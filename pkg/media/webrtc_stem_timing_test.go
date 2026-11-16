package media

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4/seekablebuffer"
	"github.com/pion/webrtc/v4"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/muxl"
	"stream.place/streamplace/pkg/spmetrics"
)

func signedOffsetAudio(t *testing.T, mainOffset, stemOffset time.Duration) []byte {
	t.Helper()
	input, err := os.ReadFile(getFixture("h264-opus-frag.mp4"))
	require.NoError(t, err)
	main, err := muxl.RunMuxlCanonicalize(t.Context(), input, nil)
	require.NoError(t, err)
	stem, err := muxl.RunMuxlCanonicalize(t.Context(), input, map[uint32]uint32{2: 3})
	require.NoError(t, err)
	events, err := unwrapMuxlEvents(t.Context(), stem)
	require.NoError(t, err)
	combined := append(main, concatTrackAcrossSegments(events, "3")...)
	events, err = unwrapMuxlEvents(t.Context(), combined)
	require.NoError(t, err)
	var header bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrapInit(t.Context(), bytes.NewReader(combined), &header))
	catalog := catalogFromEvents(events)
	scales := map[string]uint32{}
	for _, config := range catalog.Video.Renditions {
		scales[fmt.Sprint(config.TrackID())] = config.Timescale()
	}
	for _, config := range catalog.Audio.Renditions {
		scales[fmt.Sprint(config.TrackID())] = config.Timescale()
	}
	tracks := map[string]*fmp4.PartTrack{}
	for _, event := range events {
		for id, data := range event.Tracks {
			samples := event.Samples[id]
			if len(samples.Sizes) == 0 {
				continue
			}
			track := tracks[id]
			if track == nil {
				n, err := strconv.Atoi(id)
				require.NoError(t, err)
				offset := time.Duration(0)
				if id == "2" {
					offset = mainOffset
				}
				if id == "3" {
					offset = stemOffset
				}
				track = &fmp4.PartTrack{ID: n, BaseTime: event.FirstDecodeTimes[id] + uint64(offset)*uint64(scales[id])/uint64(time.Second)}
				tracks[id] = track
			}
			for i, offset := range samples.Offsets {
				track.Samples = append(track.Samples, &fmp4.Sample{Payload: data[offset : offset+uint64(samples.Sizes[i])], Duration: samples.Durations[i], PTSOffset: samples.CtsOffsets[i]})
			}
		}
	}
	tracks["1"].Samples = tracks["1"].Samples[:1]
	tracks["1"].Samples[0].Duration = 2 * scales["1"]
	var presentation seekablebuffer.Buffer
	_, err = presentation.Write(header.Bytes())
	require.NoError(t, err)
	part := fmp4.Part{Tracks: []*fmp4.PartTrack{tracks["1"], tracks["2"], tracks["3"]}}
	require.NoError(t, part.Marshal(&presentation))
	signer := newBareSegmentSigner(t)
	signing, err := signer.SigningInput()
	require.NoError(t, err)
	signing.TrackMeta = map[uint32]muxl.TrackMeta{
		2: {AlternateGroup: 1, Label: "Mix"},
		3: {AlternateGroup: 2, Label: "Stem"},
	}
	signing.TrackManifest, err = signer.buildManifest(t.Context(), time.Now().UnixMilli())
	require.NoError(t, err)
	signing.WrapperManifest = signing.TrackManifest
	events, err = collectMuxlEvents(func(out chan *muxl.MuxlEvent) error {
		return muxl.RunMuxlSignSegment(t.Context(), bytes.NewReader(presentation.Bytes()), signing, nil, nil, out)
	})
	require.NoError(t, err)
	for _, event := range events {
		if event.Type == "signed-segment" {
			segment := concatTracksSorted(event.Tracks)
			_, err := ValidateMP4Media(t.Context(), segment)
			require.NoError(t, err, "offset fixture must retain valid signatures and native media validation")
			source, err := unwrapMuxlEvents(t.Context(), segment)
			require.NoError(t, err)
			starts := map[string]uint64{}
			for _, event := range source {
				for id, start := range event.FirstDecodeTimes {
					if _, exists := starts[id]; !exists {
						starts[id] = start
					}
				}
			}
			require.InDelta(t, float64(stemOffset-mainOffset), (float64(starts["3"])-float64(starts["2"]))*float64(time.Second)/48000, float64(time.Millisecond), "fixture has signed, distinct track clocks")
			return segment
		}
	}
	t.Fatal("signer emitted no source segment")
	return nil
}

type stemRTP struct {
	id        string
	timestamp uint32
	arrival   time.Time
}

func stemPlaybackReceiver(t *testing.T, ctx context.Context, manager *MediaManager, packet *bus.PacketizedSegment) <-chan stemRTP {
	t.Helper()
	manager.bus.PublishSegment(ctx, t.Name(), WebRTCSourceRendition, &bus.Seg{Published: true, PacketizedData: packet})
	client, err := manager.webrtcAPI.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	for range 2 {
		_, err := client.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
		require.NoError(t, err)
	}
	received := make(chan stemRTP, 512)
	client.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			select {
			case received <- stemRTP{id: track.ID(), timestamp: packet.Timestamp, arrival: time.Now()}:
			case <-ctx.Done():
				return
			}
		}
	})
	offer, err := client.CreateOffer(nil)
	require.NoError(t, err)
	require.NoError(t, client.SetLocalDescription(offer))
	select {
	case <-webrtc.GatheringCompletePromise(client):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	answer, err := manager.WebRTCPlayback2(ctx, t.Name(), "audio", client.LocalDescription(), "")
	require.NoError(t, err)
	require.NoError(t, client.SetRemoteDescription(*answer))
	return received
}

func TestWebRTCPlaybackPreservesSignedStemStartOffset(t *testing.T) {
	for _, offset := range []time.Duration{500 * time.Millisecond, -500 * time.Millisecond} {
		t.Run(offset.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			mainOffset, stemOffset := max(-offset, 0), max(offset, 0)
			var packet *bus.PacketizedSegment
			withNoGSTLeaks(t, func() {
				segment := signedOffsetAudio(t, mainOffset, stemOffset)
				var err error
				packet, err = Packetize(ctx, &config.CLI{}, &bus.Seg{Muxl: segment}, 1)
				require.NoError(t, err)
			})
			require.Len(t, packet.Stems, 1)
			received := stemPlaybackReceiver(t, ctx, loopbackPlaybackManager(), packet)
			first := map[string]time.Time{}
			for len(first) < 2 {
				select {
				case item := <-received:
					if first[item.id].IsZero() {
						first[item.id] = item.arrival
					}
				case <-ctx.Done():
					t.Fatal("did not receive main and stem audio")
				}
			}
			require.InDelta(t, float64(offset), float64(first["audio-g2"].Sub(first["audio"])), float64(100*time.Millisecond),
				"a signed relative track start must survive packetization and RTP pacing")
		})
	}
}

func TestWebRTCPlaybackPreservesMissingStemSegmentAndAudioTail(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	fixture := playbackPacketFixture(t)
	samples := make([]bus.PacketizedSample, 5)
	for i := range samples {
		samples[i] = bus.PacketizedSample{Data: fixture.Audio[i].Data, Duration: 20 * time.Millisecond}
	}
	packet := &bus.PacketizedSegment{
		Duration: 100 * time.Millisecond, Audio: samples,
		Stems: []bus.PacketizedStem{{Group: 2, Audio: samples}},
	}
	manager := loopbackPlaybackManager()
	received := stemPlaybackReceiver(t, ctx, manager, packet)
	var stem []stemRTP
	main := 0
	for len(stem) < 10 {
		select {
		case item := <-received:
			if item.id == "audio-g2" {
				stem = append(stem, item)
				if len(stem) == 1 {
					manager.bus.PublishSegment(ctx, t.Name(), WebRTCSourceRendition, &bus.Seg{
						Published: true, AudioOnly: true, PacketizedData: &bus.PacketizedSegment{Duration: 40 * time.Millisecond, Audio: samples[:2]},
					})
				}
			} else if item.id == "audio" {
				main++
				if main == 6 {
					manager.bus.PublishSegment(ctx, t.Name(), WebRTCSourceRendition, &bus.Seg{
						Published: true, PacketizedData: &bus.PacketizedSegment{Duration: 60 * time.Millisecond, Audio: samples[2:]},
					})
				}
				if main == 9 {
					manager.bus.PublishSegment(ctx, t.Name(), WebRTCSourceRendition, &bus.Seg{Published: true, PacketizedData: packet})
				}
			}
		case <-ctx.Done():
			t.Fatal("stem did not resume after the missing segment")
		}
	}
	require.EqualValues(t, 9600, stem[5].timestamp-stem[0].timestamp, "the RTP clock retains the absent 100 ms rather than compressing the source timeline")
	require.Greater(t, stem[9].arrival.Sub(stem[5].arrival), 50*time.Millisecond, "resumed stem samples must be paced rather than burst against an overdue deadline")
}

func TestWebRTCPlaybackDoesNotWaitForSignedStemOverhang(t *testing.T) {
	for _, offset := range []time.Duration{500 * time.Millisecond, -500 * time.Millisecond} {
		t.Run(offset.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			var packet *bus.PacketizedSegment
			withNoGSTLeaks(t, func() {
				segment := signedOffsetAudio(t, max(-offset, 0), max(offset, 0))
				var err error
				packet, err = Packetize(ctx, &config.CLI{}, &bus.Seg{Muxl: segment}, 1)
				require.NoError(t, err)
			})
			require.Len(t, packet.Stems, 1)
			span := packet.Stems[0].Offset
			for _, sample := range packet.Stems[0].Audio {
				span += sample.Duration
			}
			id, count := "audio", len(packet.Audio)
			if offset > 0 {
				require.Greater(t, span, packet.Duration, "valid signed stem audio can continue beyond main")
			} else {
				require.Less(t, span, packet.Duration, "valid signed main audio can continue beyond a stem")
				id, count = "audio-g2", len(packet.Stems[0].Audio)
			}
			manager := loopbackPlaybackManager()
			received := stemPlaybackReceiver(t, ctx, manager, packet)
			var arrivals []time.Time
			for len(arrivals) < count+5 {
				select {
				case item := <-received:
					if item.id != id {
						continue
					}
					arrivals = append(arrivals, item.arrival)
					if len(arrivals) == 1 {
						manager.bus.PublishSegment(ctx, t.Name(), WebRTCSourceRendition, &bus.Seg{Published: true, PacketizedData: packet})
					}
				case <-ctx.Done():
					t.Fatal("audio did not progress across signed source packets")
				}
			}
			require.Greater(t, arrivals[count+4].Sub(arrivals[count]), 50*time.Millisecond,
				"one track's later signed end must not hold up another and burst its next packet")
		})
	}
}

func TestWebRTCPlaybackCancelsAnUnserviceableStemQueue(t *testing.T) {
	fixture := playbackPacketFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	manager := loopbackPlaybackManager()
	ignore := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, ignore) })
	verbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "3"))
	t.Cleanup(func() { require.NoError(t, flag.Set("v", verbosity)) })
	var logs logCapture
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	samples := make([]bus.PacketizedSample, 5)
	for i := range samples {
		samples[i] = bus.PacketizedSample{Data: fixture.Audio[i].Data, Duration: 20 * time.Millisecond}
	}
	packet := &bus.PacketizedSegment{Duration: 100 * time.Millisecond, Audio: samples,
		Stems: []bus.PacketizedStem{{Group: 2, Offset: 10 * time.Second, Audio: samples}}}
	received := stemPlaybackReceiver(t, ctx, manager, packet)
	select {
	case <-received:
	case <-ctx.Done():
		t.Fatal("main did not start while the future stem buffered")
	}
	for range 6 {
		manager.bus.PublishSegment(ctx, t.Name(), WebRTCSourceRendition, &bus.Seg{Published: true, PacketizedData: packet})
		time.Sleep(5 * time.Millisecond)
	}
	require.Never(t, func() bool { return strings.Contains(logs.String(), "exceeded bounded lookahead") }, 200*time.Millisecond, 10*time.Millisecond,
		"bounded backpressure must allow queued main playback before the next deadline")
	require.Eventually(t, func() bool { return strings.Contains(logs.String(), "exceeded bounded lookahead") }, time.Second, 10*time.Millisecond)
	subscriptions := spmetrics.SegmentSubscriptionsOpen.WithLabelValues(t.Name(), WebRTCSourceRendition)
	var metric dto.Metric
	require.Eventually(t, func() bool { return subscriptions.Write(&metric) == nil && metric.GetGauge().GetValue() == 0 }, time.Second, time.Millisecond,
		"controlled queue failure releases the playback subscription and its senders")
	require.Equal(t, 1, strings.Count(logs.String(), "exceeded bounded lookahead"))
	require.NotContains(t, logs.String(), "level=ERROR")
}

func TestWebRTCPlaybackCountsQueuedTrackJobsInBacklog(t *testing.T) {
	fixture := playbackPacketFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	var logs logCapture
	verbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "4"))
	t.Cleanup(func() { require.NoError(t, flag.Set("v", verbosity)) })
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	samples := make([]bus.PacketizedSample, 10)
	for i := range samples {
		samples[i] = bus.PacketizedSample{Data: fixture.Audio[i].Data, Duration: 20 * time.Millisecond}
	}
	packet := &bus.PacketizedSegment{Duration: 200 * time.Millisecond, Audio: samples}
	manager := loopbackPlaybackManager()
	received := stemPlaybackReceiver(t, ctx, manager, packet)
	select {
	case <-received:
	case <-ctx.Done():
		t.Fatal("initial audio did not start")
	}
	queued := make([]bus.PacketizedSample, 3000)
	for i := range queued {
		queued[i] = samples[i%len(samples)]
	}
	manager.bus.PublishSegment(ctx, t.Name(), WebRTCSourceRendition, &bus.Seg{Published: true,
		PacketizedData: &bus.PacketizedSegment{Duration: time.Minute, Audio: queued}})
	require.Eventually(t, func() bool { return strings.Count(logs.String(), "playback backlog") >= 2 }, 100*time.Millisecond, time.Millisecond)
	manager.bus.PublishSegment(ctx, t.Name(), WebRTCSourceRendition, &bus.Seg{Published: true, PacketizedData: packet})
	require.Eventually(t, func() bool {
		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, "playback backlog") && strings.Contains(line, "backlog=1m0s") && strings.Contains(line, "scalar=1.5") {
				return true
			}
		}
		return false
	}, 100*time.Millisecond, time.Millisecond, "lookahead jobs remain in the real playback backlog until their primary sender starts")
	require.NotContains(t, logs.String(), "level=ERROR")
}
