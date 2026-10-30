package media

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/renditions"
	"stream.place/streamplace/pkg/spmetrics"
)

// A finite cached GoP must survive a delayed handshake, and its predecessor
// must not be replayed to a viewer joining the live edge.
func TestWebRTCPlaybackStartsWithLatestCachedSegment(t *testing.T) {
	var fixture *bus.PacketizedSegment
	withNoGSTLeaks(t, func() { fixture = playbackPacketFixture(t) })
	older := fixture.Audio[0].Data
	var latest []byte
	for _, sample := range fixture.Audio[1:] {
		if !bytes.Equal(sample.Data, older) {
			latest = sample.Data
			break
		}
	}
	require.NotEmpty(t, latest, "fixture needs distinguishable encoded Opus packets")
	for _, delay := range []time.Duration{0, 3 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			manager := loopbackPlaybackManager()
			ignore := goleak.IgnoreCurrent()
			defer goleak.VerifyNone(t, ignore)
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			for _, data := range [][]byte{older, latest} {
				packet := &bus.PacketizedSegment{Duration: time.Second}
				for range 50 {
					packet.Audio = append(packet.Audio, bus.PacketizedSample{Data: data, Duration: 20 * time.Millisecond})
				}
				manager.bus.PublishSegment(ctx, t.Name(), WebRTCSourceRendition, &bus.Seg{Published: true, PacketizedData: packet})
			}
			receiver := createPlaybackReceiver(t, ctx, manager.webrtcAPI, renditions.AudioRendition.Name)
			defer receiver.Close()
			received := make(chan []byte, 1)
			receiver.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
				packet, _, err := track.ReadRTP()
				if err == nil {
					received <- packet.Payload
				}
			})
			answer, err := manager.WebRTCPlayback2(ctx, t.Name(), renditions.AudioRendition.Name, receiver.LocalDescription(), "")
			require.NoError(t, err)
			time.Sleep(delay)
			require.NoError(t, receiver.SetRemoteDescription(*answer))
			select {
			case payload := <-received:
				require.Equal(t, latest, payload, "first RTP must belong to the newest cached segment")
			case <-ctx.Done():
				t.Fatal("finite cached media was consumed before the receiver connected")
			}
		})
	}
}

func TestWebRTCPlaybackCancelsPendingHandshake(t *testing.T) {
	manager := loopbackPlaybackManager()
	ignore := goleak.IgnoreCurrent()
	defer goleak.VerifyNone(t, ignore)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	receiver := createPlaybackReceiver(t, ctx, manager.webrtcAPI, renditions.AudioRendition.Name)
	defer receiver.Close()
	answer, err := manager.WebRTCPlayback2(ctx, t.Name(), renditions.AudioRendition.Name, receiver.LocalDescription(), "")
	require.NoError(t, err)
	require.NotNil(t, answer)
	subscriptions := spmetrics.SegmentSubscriptionsOpen.WithLabelValues(t.Name(), WebRTCSourceRendition)
	require.Never(t, func() bool {
		var metric dto.Metric
		return subscriptions.Write(&metric) != nil || metric.GetGauge().GetValue() != 0
	}, 100*time.Millisecond, time.Millisecond, "pending handshakes must not consume live segments")
	require.Zero(t, manager.bus.GetViewerCount(t.Name()))
	cancel()
	require.NoError(t, receiver.Close())
	var metric dto.Metric
	require.NoError(t, subscriptions.Write(&metric))
	require.Zero(t, metric.GetGauge().GetValue())
	require.Zero(t, manager.bus.GetViewerCount(t.Name()))
}

func TestWebRTCPlaybackPacesAfterUpstreamIdle(t *testing.T) {
	var fixture *bus.PacketizedSegment
	withNoGSTLeaks(t, func() { fixture = playbackPacketFixture(t) })
	manager := loopbackPlaybackManager()
	ignore := goleak.IgnoreCurrent()
	defer goleak.VerifyNone(t, ignore)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	packet := &bus.PacketizedSegment{Duration: 200 * time.Millisecond}
	for range 10 {
		packet.Audio = append(packet.Audio, bus.PacketizedSample{Data: fixture.Audio[0].Data, Duration: 20 * time.Millisecond})
	}
	publish := func() {
		manager.bus.PublishSegment(ctx, t.Name(), WebRTCSourceRendition, &bus.Seg{Published: true, PacketizedData: packet})
	}
	publish()
	receiver := createPlaybackReceiver(t, ctx, manager.webrtcAPI, renditions.AudioRendition.Name)
	defer receiver.Close()
	arrivals := make(chan time.Time, 20)
	receiver.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			if _, _, err := track.ReadRTP(); err != nil {
				return
			}
			select {
			case arrivals <- time.Now():
			case <-ctx.Done():
				return
			}
		}
	})
	answer, err := manager.WebRTCPlayback2(ctx, t.Name(), renditions.AudioRendition.Name, receiver.LocalDescription(), "")
	require.NoError(t, err)
	require.NoError(t, receiver.SetRemoteDescription(*answer))
	readGoP := func() time.Duration {
		var first, last time.Time
		for i := range 10 {
			select {
			case last = <-arrivals:
				if i == 0 {
					first = last
				}
			case <-ctx.Done():
				t.Fatal("finite audio GoP did not arrive")
			}
		}
		return last.Sub(first)
	}
	readGoP()
	// The finite first GoP drains before new source media becomes available.
	time.Sleep(300 * time.Millisecond)
	publish()
	require.GreaterOrEqual(t, readGoP(), 120*time.Millisecond, "upstream idle must not make the next 200ms GoP arrive as an overdue burst")
	cancel()
	require.NoError(t, receiver.Close())
	subscriptions := spmetrics.SegmentSubscriptionsOpen.WithLabelValues(t.Name(), WebRTCSourceRendition)
	require.Eventually(t, func() bool {
		var metric dto.Metric
		return subscriptions.Write(&metric) == nil && metric.GetGauge().GetValue() == 0
	}, time.Second, time.Millisecond)
}
