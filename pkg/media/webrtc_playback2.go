package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/log"
	"stream.place/streamplace/pkg/renditions"
)

// This function remains in scope for the duration of a single users' playback
func (mm *MediaManager) WebRTCPlayback2(ctx context.Context, user string, rendition string, offer *webrtc.SessionDescription, viewer string) (*webrtc.SessionDescription, error) {
	uu, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	ctx = log.WithLogValues(ctx, "webrtcID", uu.String())
	ctx = log.WithLogValues(ctx, "mediafunc", "WebRTCPlayback")

	// Create a new RTCPeerConnection
	peerConnection, err := mm.webrtcAPI.NewPeerConnection(mm.webrtcConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create WebRTC peer connection: %w", err)
	}

	audioOnly := rendition == renditions.AudioRendition.Name

	var videoTrack *webrtc.TrackLocalStaticSample
	var videoRTPSender *webrtc.RTPSender
	if !audioOnly {
		videoTrack, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "pion")
		if err != nil {
			return nil, fmt.Errorf("failed to create video track: %w", err)
		}
		videoRTPSender, err = peerConnection.AddTrack(videoTrack)
		if err != nil {
			return nil, fmt.Errorf("failed to add video track to peer connection: %w", err)
		}
	}

	audioTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "pion")
	if err != nil {
		return nil, fmt.Errorf("failed to create audio track: %w", err)
	}
	audioRTPSender, err := peerConnection.AddTrack(audioTrack)
	if err != nil {
		return nil, fmt.Errorf("failed to add audio track to peer connection: %w", err)
	}

	audioSenders := []*webrtc.RTPSender{audioRTPSender}
	stemTracks := map[uint32]*webrtc.TrackLocalStaticSample{}
	busRendition := rendition
	if audioOnly || rendition == "source" {
		busRendition = WebRTCSourceRendition
	}
	latest := mm.bus.LatestSegment(user, busRendition)
	if latest != nil && !latest.Published && viewer != user && !mm.cli.WideOpen {
		latest = nil
	}
	for _, group := range stemGroupsFor(latest, offeredAudioTracks(offer)-1) {
		track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, fmt.Sprintf("audio-g%d", group), "pion")
		if err != nil {
			_ = peerConnection.Close()
			return nil, err
		}
		sender, err := peerConnection.AddTrack(track)
		if err != nil {
			_ = peerConnection.Close()
			return nil, fmt.Errorf("add stem track: %w", err)
		}
		audioSenders = append(audioSenders, sender)
		stemTracks[group] = track
	}

	closePeerConnection := func() {
		if cErr := peerConnection.Close(); cErr != nil {
			log.Log(ctx, "cannot close peerConnection: %v\n", cErr)
		}
	}

	// Set the remote SessionDescription
	if err = peerConnection.SetRemoteDescription(*offer); err != nil {
		closePeerConnection()
		return nil, fmt.Errorf("failed to set remote description: %w", err)
	}

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		closePeerConnection()
		return nil, fmt.Errorf("failed to create answer: %w", err)
	}

	// Sets the LocalDescription, and starts our UDP listeners
	if err = peerConnection.SetLocalDescription(answer); err != nil {
		closePeerConnection()
		return nil, fmt.Errorf("failed to set local description: %w", err)
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	// Setup complete! Now we boot up streaming in the background while returning the SDP offer to the user.

	// The session only counts as a viewer once the peer connection actually
	// establishes — counting at SDP-answer time inflated the count with
	// handshakes that never connected (each lingering until ICE failure
	// detection). The mutex pairs the increment with exactly one decrement
	// even if a connect races session teardown.
	var viewerMu sync.Mutex
	viewerCounted := false
	viewerDone := false
	connected := make(chan struct{})
	markConnected := func() {
		viewerMu.Lock()
		defer viewerMu.Unlock()
		if viewerDone || viewerCounted {
			return
		}
		viewerCounted = true
		mm.IncrementViewerCount(user, "webrtc")
		close(connected)
	}
	markDone := func() {
		viewerMu.Lock()
		defer viewerMu.Unlock()
		viewerDone = true
		if viewerCounted {
			viewerCounted = false
			mm.DecrementViewerCount(user, "webrtc")
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Log(ctx, "Peer Connection State has changed", "state", state.String())
		if state == webrtc.PeerConnectionStateConnected {
			markConnected()
		}
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed || state == webrtc.PeerConnectionStateDisconnected {
			log.Log(ctx, "Peer Connection has gone to failed, exiting")
			cancel()
		}
	})

	go func() {
		defer cancel()
		defer markDone()

		var backlog atomic.Int64

		type playbackSegment struct {
			packet    *bus.PacketizedSegment
			audioOnly bool
		}
		packetQueue := make(chan playbackSegment, 1024)
		go func() {
			// Starting at connection time keeps finite cached media available
			// until the tracks bind, then joins the newest cached GoP.
			select {
			case <-connected:
			case <-ctx.Done():
				return
			}
			busRendition := rendition
			if audioOnly || rendition == "source" {
				busRendition = WebRTCSourceRendition
			}
			segChan := mm.bus.SubscribeSegmentBuf(ctx, user, busRendition, 1)
			defer mm.bus.UnsubscribeSegment(ctx, user, busRendition, segChan)
			for {
				select {
				case <-ctx.Done():
					log.Debug(ctx, "exiting segment reader")
					return
				case file := <-segChan.C:
					log.Debug(ctx, "got segment", "file", file.Filepath)
					if !file.Published && viewer != user && !mm.cli.WideOpen {
						log.Warn(ctx, "segment is not published and viewer is not the user", "viewer", viewer, "user", user)
						continue
					}
					backlog.Add(int64(file.PacketizedData.Duration))
					select {
					case packetQueue <- playbackSegment{packet: file.PacketizedData, audioOnly: file.AudioOnly}:
					case <-ctx.Done():
						backlog.Add(-int64(file.PacketizedData.Duration))
						return
					}
				}
			}
		}()

		go func() {
			go func() {
				<-ctx.Done()
				if cErr := peerConnection.Close(); cErr != nil {
					log.Log(ctx, "cannot close peerConnection: %v\n", cErr)
				}
			}()

			type playbackJob struct {
				samples  []bus.PacketizedSample
				scalar   float64
				start    time.Time
				position time.Duration
				duration time.Duration
			}
			startSender := func(write func(playbackJob) error) chan playbackJob {
				jobs := make(chan playbackJob, 4)
				go func() {
					for {
						select {
						case <-ctx.Done():
							return
						case job := <-jobs:
							if err := write(job); err != nil {
								if ctx.Err() == nil && !errors.Is(err, io.ErrClosedPipe) {
									log.Error(ctx, "failed to write samples", "error", err)
								}
								cancel()
								return
							}
						}
					}
				}()
				return jobs
			}
			var audioPacer samplePacer
			audioJobs := startSender(func(job playbackJob) error {
				backlog.Add(-int64(job.duration))
				audioPacer.deadline = job.start
				if !audioPacer.wait(ctx) {
					return nil
				}
				return audioPacer.writeAudioSamples(ctx, audioTrack, job.samples, job.duration, job.scalar)
			})
			var videoJobs chan playbackJob
			if !audioOnly {
				var videoPacer samplePacer
				videoJobs = startSender(func(job playbackJob) error {
					backlog.Add(-int64(job.duration))
					videoPacer.deadline = job.start
					if !videoPacer.wait(ctx) {
						return nil
					}
					return videoPacer.writeSamples(ctx, videoTrack, job.samples, job.scalar, 0)
				})
			}
			stemJobs := make(map[uint32]chan playbackJob, len(stemTracks))
			for group, track := range stemTracks {
				var pacer samplePacer
				stemJobs[group] = startSender(func(job playbackJob) error {
					return pacer.writeStemSamples(ctx, track, job.samples, job.scalar, job.start, job.position)
				})
			}
			// Look ahead without letting one track's longer signed span block another.
			enqueue := func(jobs chan playbackJob, job playbackJob, due time.Time) bool {
				select {
				case jobs <- job:
					return true
				case <-ctx.Done():
					return false
				default:
				}
				timer := time.NewTimer(max(time.Until(due), 0))
				defer timer.Stop()
				select {
				case jobs <- job:
					return true
				case <-ctx.Done():
					return false
				case <-timer.C:
					log.Warn(ctx, "playback track exceeded bounded lookahead")
					cancel()
					return false
				}
			}
			var audioDeadline, videoDeadline time.Time
			var audioPosition time.Duration
			for {
				var segment playbackSegment
				waited := false
				select {
				case <-ctx.Done():
					return
				case segment = <-packetQueue:
				default:
					waited = true
					select {
					case <-ctx.Done():
						return
					case segment = <-packetQueue:
					}
				}
				packet := segment.packet
				queuedDuration := max(time.Duration(backlog.Load())-packet.Duration, 0)
				scalar := getPlaybackRate(queuedDuration)
				log.Debug(ctx, "playback backlog", "backlog", queuedDuration, "scalar", scalar)
				now := time.Now()
				audioStart, videoStart := audioDeadline, videoDeadline
				if audioStart.IsZero() {
					var earliest time.Duration
					for _, stem := range packet.Stems {
						if stemJobs[stem.Group] != nil && len(stem.Audio) > 0 {
							earliest = min(earliest, stem.Offset)
						}
					}
					audioStart = now.Add(-time.Duration(float64(earliest) / scalar))
				}
				if videoStart.IsZero() {
					videoStart = now
				}
				var due time.Time
				include := func(start time.Time) {
					if due.IsZero() || start.Before(due) {
						due = start
					}
				}
				if len(packet.Audio) > 0 {
					include(audioStart)
				}
				if videoJobs != nil && len(packet.Video) > 0 {
					include(videoStart)
				}
				for _, stem := range packet.Stems {
					if stemJobs[stem.Group] != nil && len(stem.Audio) > 0 {
						include(audioStart.Add(time.Duration(float64(stem.Offset) / scalar)))
					}
				}
				// A source wait overlaps queued playback; only pause for real lateness.
				if waited && !due.IsZero() && now.After(due) {
					idle := now.Sub(due)
					if !audioDeadline.IsZero() {
						audioStart = audioStart.Add(idle)
					}
					if !videoDeadline.IsZero() {
						videoStart = videoStart.Add(idle)
					}
					due = now
				}
				position := audioPosition
				audioPosition += packet.Duration
				if len(packet.Audio) > 0 {
					audioDeadline = audioStart.Add(time.Duration(float64(packet.Duration) / scalar))
					if !enqueue(audioJobs, playbackJob{samples: packet.Audio, scalar: scalar, start: audioStart, duration: packet.Duration}, due) {
						return
					}
				} else {
					if videoJobs == nil || len(packet.Video) == 0 {
						backlog.Add(-int64(packet.Duration))
					}
					audioDeadline = time.Time{}
					log.Warn(ctx, "no audio samples to write")
				}
				if videoJobs != nil && len(packet.Video) > 0 {
					videoDeadline = videoStart
					for _, sample := range packet.Video {
						videoDeadline = videoDeadline.Add(time.Duration(float64(sample.Duration) / scalar))
					}
					counted := time.Duration(0)
					if len(packet.Audio) == 0 {
						counted = packet.Duration
					}
					if !enqueue(videoJobs, playbackJob{samples: packet.Video, scalar: scalar, start: videoStart, duration: counted}, due) {
						return
					}
				} else if !audioOnly && !segment.audioOnly {
					videoDeadline = time.Time{}
					log.Warn(ctx, "no video samples to write")
				}
				for _, stem := range packet.Stems {
					if jobs := stemJobs[stem.Group]; jobs != nil && len(stem.Audio) > 0 {
						if !enqueue(jobs, playbackJob{samples: stem.Audio, scalar: scalar, start: audioStart.Add(time.Duration(float64(stem.Offset) / scalar)), position: position + stem.Offset}, due) {
							return
						}
					}
				}
			}

		}()

		if !audioOnly {
			go func() {
				rtcpBuf := make([]byte, 1500)
				for {
					if _, _, rtcpErr := videoRTPSender.Read(rtcpBuf); rtcpErr != nil {
						return
					}
				}
			}()
		}

		for _, sender := range audioSenders {
			go func() {
				rtcpBuf := make([]byte, 1500)
				for {
					if _, _, err := sender.Read(rtcpBuf); err != nil {
						return
					}
				}
			}()
		}

		// Set the handler for ICE connection state
		// This will notify you when the peer has connected/disconnected
		peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
			log.Log(ctx, "Connection State has changed", "state", connectionState.String())
		})

		<-ctx.Done()

		log.Warn(ctx, "exiting playback")

	}()
	select {
	case <-gatherComplete:
		return peerConnection.LocalDescription(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type samplePacer struct {
	deadline time.Time
	timer    *time.Timer
	position time.Duration
}

// offeredAudioTracks bounds senders by the receiver's offered transceivers.
func offeredAudioTracks(offer *webrtc.SessionDescription) int {
	parsed, err := offer.Unmarshal()
	if err != nil {
		return 1
	}
	n := 0
	for _, md := range parsed.MediaDescriptions {
		if md.MediaName.Media == "audio" {
			n++
		}
	}
	return max(n, 1)
}

func stemGroupsFor(latest *bus.Seg, limit int) []uint32 {
	if latest == nil || latest.PacketizedData == nil || limit <= 0 {
		return nil
	}
	var groups []uint32
	for _, stem := range latest.PacketizedData.Stems {
		groups = append(groups, stem.Group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i] < groups[j] })
	return groups[:min(len(groups), limit, maxStems)]
}

// Split the audio span uniformly so timestamp gaps do not expand its RTP clock.
func (p *samplePacer) writeAudioSamples(ctx context.Context, track *webrtc.TrackLocalStaticSample, samples []bus.PacketizedSample, span time.Duration, scalar float64) error {
	if len(samples) == 0 {
		return nil
	}
	duration := span / time.Duration(len(samples))
	if duration <= 0 {
		return nil
	}
	return p.writeSamples(ctx, track, samples, scalar, duration)
}

// Stems share the main schedule, including source offsets and omitted packets.
// Empty samples advance the RTP clock without emitting or re-encoding audio.
func (p *samplePacer) writeStemSamples(ctx context.Context, track *webrtc.TrackLocalStaticSample, samples []bus.PacketizedSample, scalar float64, start time.Time, position time.Duration) error {
	if gap := position - p.position; gap > 0 {
		if err := track.WriteSample(media.Sample{Duration: gap}); err != nil {
			return fmt.Errorf("advance stem RTP clock: %w", err)
		}
	}
	p.deadline = start
	if !p.wait(ctx) {
		return nil
	}
	if err := p.writeSamples(ctx, track, samples, scalar, 0); err != nil {
		return err
	}
	p.position = position
	for _, sample := range samples {
		p.position += sample.Duration
	}
	return nil
}

func (p *samplePacer) wait(ctx context.Context) bool {
	wait := time.Until(p.deadline)
	if wait <= 0 {
		return ctx.Err() == nil
	}
	if p.timer == nil {
		p.timer = time.NewTimer(wait)
	} else {
		p.timer.Reset(wait)
	}
	select {
	case <-ctx.Done():
		p.timer.Stop()
		return false
	case <-p.timer.C:
		return true
	}
}

// writeSamples writes one track's samples paced by their real durations —
// the source timeline, so non-uniform frame spacing (bursts and gaps from an
// encoder shedding frames) reaches the viewer intact. The stamped duration
// stays real even while catch-up (scalar > 1) speeds the pacing: the
// receiver's clock is authoritative for playout, sending faster just refills
// its buffer.
//
// Pacing is against a wall-clock schedule, NOT a per-sample sleep: each sleep
// overshoots by scheduler latency, and chaining sleeps accumulates that
// overshoot into real drift (hundreds of ms per segment at high sample rates
// — enough to starve the receiver's jitter buffer). Sleeping until the
// scheduled deadline instead absorbs overshoot in the next iteration, like a
// ticker does.
func (p *samplePacer) writeSamples(ctx context.Context, track *webrtc.TrackLocalStaticSample, samples []bus.PacketizedSample, scalar float64, uniformDuration time.Duration) error {
	if p.deadline.IsZero() {
		p.deadline = time.Now()
	}
	for _, s := range samples {
		duration := s.Duration
		if uniformDuration > 0 {
			duration = uniformDuration
		}
		if err := track.WriteSample(media.Sample{Data: s.Data, Duration: duration}); err != nil {
			return fmt.Errorf("failed to write sample: %w", err)
		}
		p.deadline = p.deadline.Add(time.Duration(float64(duration) / scalar))
		if !p.wait(ctx) {
			return nil
		}
	}
	return nil
}

// getPlaybackRate returns a playback rate that eases from 1.0 to 1.5 between 7 and 60 seconds
func getPlaybackRate(dur time.Duration) float64 {
	switch {
	case dur <= 7*time.Second:
		return 1.0
	case dur >= 60*time.Second:
		return 1.5
	default:
		// Linear interpolation between (7,1.0) and (60,1.5)
		progress := (float64(dur) - float64(7*time.Second)) / (float64(60*time.Second) - float64(7*time.Second))
		return 1.0 + (0.5 * progress)
	}
}
