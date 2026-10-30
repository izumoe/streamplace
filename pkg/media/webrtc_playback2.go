package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"golang.org/x/sync/errgroup"
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

		packetQueue := make(chan *bus.PacketizedSegment, 1024)
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
					case packetQueue <- file.PacketizedData:
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

			var videoPacer, audioPacer samplePacer
			for {
				var packet *bus.PacketizedSegment
				select {
				case <-ctx.Done():
					return
				case packet = <-packetQueue:
				default:
					// Pause both clocks only while no media is available. A source
					// stall must not become a burst of overdue samples on recovery.
					started := time.Now()
					select {
					case <-ctx.Done():
						return
					case packet = <-packetQueue:
					}
					idle := time.Since(started)
					videoPacer.pause(idle)
					audioPacer.pause(idle)
				}

				queuedDuration := time.Duration(backlog.Add(-int64(packet.Duration)))
				scalar := getPlaybackRate(queuedDuration)
				log.Debug(ctx, "playback backlog", "backlog", queuedDuration, "scalar", scalar)
				g, groupCtx := errgroup.WithContext(ctx)

				if !audioOnly && len(packet.Video) > 0 {
					g.Go(func() error {
						return videoPacer.writeSamples(groupCtx, videoTrack, packet.Video, scalar, 0)
					})
				} else if !audioOnly {
					videoPacer.deadline = time.Time{}
					log.Warn(ctx, "no video samples to write")
				}
				if len(packet.Audio) > 0 {
					g.Go(func() error {
						return audioPacer.writeAudioSamples(groupCtx, audioTrack, packet, scalar)
					})
				} else {
					audioPacer.deadline = time.Time{}
					log.Warn(ctx, "no audio samples to write")
				}
				if err := g.Wait(); err != nil {
					if ctx.Err() == nil || !errors.Is(err, io.ErrClosedPipe) {
						log.Error(ctx, "failed to write samples", "error", err)
					}
					cancel()
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

		go func() {
			rtcpBuf := make([]byte, 1500)
			for {
				if _, _, rtcpErr := audioRTPSender.Read(rtcpBuf); rtcpErr != nil {
					return
				}
			}
		}()

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
}

func (p *samplePacer) pause(idle time.Duration) {
	if !p.deadline.IsZero() {
		p.deadline = p.deadline.Add(idle)
	}
}

// Split the audio span uniformly so timestamp gaps do not expand its RTP clock.
func (p *samplePacer) writeAudioSamples(ctx context.Context, track *webrtc.TrackLocalStaticSample, packet *bus.PacketizedSegment, scalar float64) error {
	if len(packet.Audio) == 0 {
		return nil
	}
	duration := packet.Duration / time.Duration(len(packet.Audio))
	if duration <= 0 {
		return nil
	}
	return p.writeSamples(ctx, track, packet.Audio, scalar, duration)
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
		wait := time.Until(p.deadline)
		if wait <= 0 {
			continue
		}
		if p.timer == nil {
			p.timer = time.NewTimer(wait)
		} else {
			p.timer.Reset(wait)
		}
		select {
		case <-ctx.Done():
			p.timer.Stop()
			return nil
		case <-p.timer.C:
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
