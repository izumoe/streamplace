package media

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
)

func TestAudioAddendumCannotOvertakeSourceDistribution(t *testing.T) {
	ctx := t.Context()
	signer := audioSignerForTest(t)
	completed, tail := audioAddendumSourceForTest(t, signer)
	require.GreaterOrEqual(t, len(completed), 2)
	mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
	did := signer.Streamer()
	for _, source := range completed[:len(completed)-1] {
		valid, err := ValidateMP4MediaC2PA(ctx, source)
		require.NoError(t, err)
		mm.bufferSegment(did, valid.Meta.StartTime.Time(), valid.Meta.Published, false, source, 0, *valid.Manifest.Label)
		mm.feedLiveWindow(ctx, did, source, valid.Meta.StartTime.Time(), valid.Meta.Published)
	}
	window, _ := mm.GetLiveWindow(did)
	require.NotNil(t, window)
	entered, release, playlistDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(playlistDone)
		first := true
		window.MasterPlaylist(func(id string) string {
			if first {
				first = false
				close(entered)
				<-release
			}
			return id + ".m3u8"
		})
	}()
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() {
		unblock()
		waitLiveWindowWork(t, playlistDone)
	})
	waitLiveWindowWork(t, entered)

	source := completed[len(completed)-1]
	valid, err := ValidateMP4MediaC2PA(ctx, source)
	require.NoError(t, err)
	notifications := mm.NewSegment()
	distributed := make(chan error, 1)
	go func() {
		distributed <- mm.distributeSegment(ctx, &validatedSegment{
			repoDID: did, label: *valid.Manifest.Label, meta: valid.Meta,
		}, source, nil)
	}()
	select {
	case err := <-distributed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("HLS blocked source distribution")
	}
	waitLiveWindowStack(t, "(*MediaManager).feedLiveWindowEvents", "(*Writer).Observe")

	// The source must be queued before an independently arriving tail can bind
	// to its buffer, even while the asynchronous HLS feed is still blocked.
	require.NoError(t, mm.ReceiveAudioAddendum(ctx, did, tail))
	for _, audioOnly := range []bool{false, true} {
		select {
		case notification := <-notifications:
			require.Equal(t, audioOnly, notification.AudioOnly, "source notification must precede its audio continuation")
			require.Equal(t, *valid.Manifest.Label, notification.Segment.ID)
		case <-time.After(time.Second):
			t.Fatal("source or audio continuation was not queued")
		}
	}
	unblock()
	waitLiveWindowWork(t, playlistDone)
	buffered := mm.BufferedSegments(did)
	waitLiveWindowWork(t, buffered[len(buffered)-1].hlsReady)
}
