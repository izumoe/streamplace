package media

import (
	"bytes"
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/muxl"
)

func TestPlaybackAddendaWaitForSourceHLSWithoutHoldingWindow(t *testing.T) {
	ctx := t.Context()
	signer := audioSignerForTest(t)
	completed, tail := audioAddendumSourceForTest(t, signer)
	last := completed[len(completed)-1]
	valid, err := ValidateMP4MediaC2PA(ctx, last)
	require.NoError(t, err)
	var flat bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(last), "flat", &flat))
	cert, key := nodeSignerForTest(t)
	mint := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
	video, err := mint.mintVideoRenditions(ctx, last, []RenditionInput{{MP4: flat.Bytes()}}, cert, key)
	require.NoError(t, err)
	for _, test := range []struct {
		name              string
		rendition, cancel bool
	}{{"audio-tail", false, false}, {"video-rendition", true, false}, {"cancelled-audio-tail", false, true}} {
		rendition := test.rendition
		t.Run(test.name, func(t *testing.T) {
			callCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
			mm.bus = bus.NewBus()
			did := signer.Streamer()
			for i, data := range completed {
				source, err := ValidateMP4MediaC2PA(ctx, data)
				require.NoError(t, err)
				mm.bufferSegment(did, source.Meta.StartTime.Time(), true, false, data, 0, *source.Manifest.Label)
				events, err := unwrapMuxlEvents(ctx, data)
				require.NoError(t, err)
				mm.rememberSourceAudio(did, *source.Manifest.Label, events)
				if i < len(completed)-1 {
					mm.feedLiveWindow(ctx, did, data, source.Meta.StartTime.Time(), true)
				}
			}
			ready := make(chan struct{})
			release := sync.OnceFunc(func() { close(ready) })
			t.Cleanup(release)
			mm.segBuffersMut.Lock()
			mm.segBuffers[did].segs[len(completed)-1].hlsReady = ready
			mm.segBuffersMut.Unlock()
			done := make(chan error, 1)
			go func() {
				if rendition {
					done <- mm.PublishRenditionsForPlayback(callCtx, did, video, true)
				} else {
					done <- mm.PublishAudioAddendum(callCtx, did, tail, *valid.Manifest.Label, valid.Meta.StartTime.Time(), true)
				}
			}()
			early := false
			var earlyErr error
			require.Eventually(t, func() bool {
				select {
				case earlyErr = <-done:
					early = true
					return true
				default:
				}
				stack := make([]byte, 1<<20)
				n := runtime.Stack(stack, true)
				return bytes.Contains(stack[:n], []byte("(*MediaManager).waitSourceHLS"))
			}, 5*time.Second, time.Millisecond)
			require.False(t, early, "addendum completed before its source HLS feed: %v", earlyErr)
			// A blocked addendum cannot hold the state lock needed by the source feed.
			windowRead := make(chan struct{})
			go func() { mm.GetLiveWindow(did); close(windowRead) }()
			select {
			case <-windowRead:
			case <-time.After(time.Second):
				t.Fatal("addendum held the source's HLS window lock")
			}
			if test.cancel {
				cancel()
				select {
				case err := <-done:
					require.ErrorIs(t, err, context.Canceled)
				case <-time.After(time.Second):
					t.Fatal("cancelled addendum kept waiting for HLS")
				}
				return
			}
			mm.feedLiveWindow(ctx, did, last, valid.Meta.StartTime.Time(), true)
			release()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("addendum did not finish after its source")
			}
			window, published := mm.GetLiveWindow(did)
			require.NotNil(t, window)
			require.True(t, published)
			if rendition {
				require.NotEmpty(t, mm.LiveRenditionNames(did))
			} else {
				found := false
				for _, id := range window.TrackIDs() {
					track := window.Track(id)
					for _, segment := range track.Segments {
						found = found || bytes.Contains(window.SegmentData(id, segment.Seq), tail)
					}
				}
				require.True(t, found, "the advertised tail must follow all original source audio")
			}
		})
	}
}
