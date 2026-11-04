package media

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPlaybackSessionCallbacksStayOrdered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for range 100 {
			entered, release := make(chan struct{}), make(chan struct{})
			touched, closed := make(chan struct{}), make(chan struct{})
			var admitted, premature atomic.Bool
			tracker := newPlaybackSessionTracker(playbackSessionTTL,
				func(string) { close(entered); <-release; admitted.Store(true) },
				func(string) {
					if !admitted.Load() {
						premature.Store(true)
					}
				},
			)
			go func() { tracker.Touch("streamer", "session"); close(touched) }()
			<-entered
			go func() { tracker.Close("streamer", "session"); close(closed) }()
			runtime.Gosched()
			close(release)
			<-touched
			<-closed
			require.False(t, premature.Load(), "close must wait for admission to finish")
		}
		time.Sleep(playbackSessionSweepInterval + time.Second)
	})
}

// countingTracker wires a tracker to a per-streamer net counter so tests can
// assert the onNew/onExpire pairing.
type countingTracker struct {
	*playbackSessionTracker
	mu     sync.Mutex
	counts map[string]int
	clock  time.Time
}

func newCountingTracker(t *testing.T) *countingTracker {
	ct := &countingTracker{
		counts: map[string]int{},
		clock:  time.Unix(1000, 0),
	}
	ct.playbackSessionTracker = newPlaybackSessionTracker(playbackSessionTTL,
		func(streamer string) {
			ct.mu.Lock()
			defer ct.mu.Unlock()
			ct.counts[streamer]++
		},
		func(streamer string) {
			ct.mu.Lock()
			defer ct.mu.Unlock()
			ct.counts[streamer]--
		},
	)
	ct.now = func() time.Time {
		ct.mu.Lock()
		defer ct.mu.Unlock()
		return ct.clock
	}
	return ct
}

func (ct *countingTracker) count(streamer string) int {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return ct.counts[streamer]
}

func (ct *countingTracker) advance(d time.Duration) {
	ct.mu.Lock()
	ct.clock = ct.clock.Add(d)
	ct.mu.Unlock()
}

func TestHLSSessionTrackerCountsDistinctSids(t *testing.T) {
	ct := newCountingTracker(t)

	ct.Touch("did:plc:streamer", "sid-1")
	require.Equal(t, 1, ct.count("did:plc:streamer"))

	// Repeat requests for the same session don't re-count.
	ct.Touch("did:plc:streamer", "sid-1")
	ct.Touch("did:plc:streamer", "sid-1")
	require.Equal(t, 1, ct.count("did:plc:streamer"))

	// A second session counts; sessions are per-streamer.
	ct.Touch("did:plc:streamer", "sid-2")
	ct.Touch("did:plc:other", "sid-1")
	require.Equal(t, 2, ct.count("did:plc:streamer"))
	require.Equal(t, 1, ct.count("did:plc:other"))

	// Requests with no sid are unattributable and ignored.
	ct.Touch("did:plc:streamer", "")
	ct.Touch("", "sid-3")
	require.Equal(t, 2, ct.count("did:plc:streamer"))
}

func TestHLSSessionTrackerExpiry(t *testing.T) {
	ct := newCountingTracker(t)

	ct.Touch("did:plc:streamer", "sid-1")
	ct.Touch("did:plc:streamer", "sid-2")
	require.Equal(t, 2, ct.count("did:plc:streamer"))

	// Within the TTL nothing expires.
	ct.advance(playbackSessionTTL / 2)
	require.False(t, ct.sweep())
	require.Equal(t, 2, ct.count("did:plc:streamer"))

	// Keep one session alive past the other's TTL.
	ct.Touch("did:plc:streamer", "sid-2")
	ct.advance(playbackSessionTTL/2 + time.Second)
	require.False(t, ct.sweep())
	require.Equal(t, 1, ct.count("did:plc:streamer"))

	// The survivor ages out too; sweep reports the tracker drained.
	ct.advance(playbackSessionTTL)
	require.True(t, ct.sweep())
	require.Equal(t, 0, ct.count("did:plc:streamer"))

	// A returning sid is a fresh session.
	ct.Touch("did:plc:streamer", "sid-1")
	require.Equal(t, 1, ct.count("did:plc:streamer"))
}
