package media

import (
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	prometheus "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/spmetrics"
)

func TestMoqViewerHandoff(t *testing.T) {
	b := bus.NewBus()
	synctest.Test(t, func(t *testing.T) {
		const did = "did:plc:moq-handoff"
		mm := &MediaManager{bus: b}
		mm.initMoqViewers()
		checkStats := func(want float64) {
			t.Helper()
			metric := &prometheus.Metric{}
			require.NoError(t, spmetrics.MoqRelayStatsAvailable.Write(metric))
			require.Equal(t, want, metric.GetGauge().GetValue())
		}
		checkStats(0)
		var views atomic.Int64
		mm.SetViewRecorder(func(string) { views.Add(1) })
		check := func(want int) {
			t.Helper()
			require.Equal(t, want, b.GetViewerCount(did))
			require.Equal(t, want, spmetrics.GetViewCount(did))
		}
		mm.IncrementViewerCount(did, "webrtc")
		mm.TouchMoqSession(did, "a")
		mm.TouchMoqSession(did, "a")
		mm.TouchMoqSession(did, "b")
		check(3)
		mm.SetMoqRelayViewers(map[string]int{did: 5})
		checkStats(1)
		check(6)
		mm.TouchMoqSession(did, "c") // fallback stays warm, never added to relay count
		check(6)
		mm.SetMoqRelayViewers(map[string]int{}) // known zero is authoritative
		checkStats(1)
		check(1)
		mm.SetMoqRelayViewers(nil) // unavailable is different from zero
		checkStats(0)
		check(4)
		mm.CloseMoqSession(did, "a")
		mm.CloseMoqSession(did, "a")
		check(3)
		time.Sleep(playbackSessionTTL + playbackSessionSweepInterval + time.Second)
		synctest.Wait()
		check(1)
		mm.DecrementViewerCount(did, "webrtc")
		check(0)
		synctest.Wait()
		require.Equal(t, int64(4), views.Load(), "stats changes must not invent lifetime views")
	})
}
