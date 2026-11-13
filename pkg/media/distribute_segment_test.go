package media

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/aqtime"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/muxl"
)

func TestReplicatedDistributionPairsAudioWhileHLSBusy(t *testing.T) {
	ctx := t.Context()
	segment := sourceSegmentForTest(t)
	original := bytes.Clone(segment)
	var presentation bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(segment), "flat", &presentation))
	const did = "did:test:replicated-distribution"
	mm := NewOffline(&config.CLI{Syndicate: []string{did}})
	started := time.Now()
	mm.feedLiveWindow(ctx, did, segment, started, true)
	window, _ := mm.GetLiveWindow(did)
	require.NotNil(t, window)
	trackID := window.TrackIDs()[0]
	before := window.Track(trackID)
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
	distributed := make(chan error, 1)
	go func() {
		distributed <- mm.distributeSegment(ctx, &validatedSegment{
			repoDID: did,
			meta:    &SegmentMetadata{StartTime: aqtime.FromTime(started.Add(time.Second)), Published: true},
		}, segment, presentation.Bytes())
	}()
	select {
	case err := <-distributed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("HLS blocked replicated distribution")
	}
	mm.sourceAudioMu.Lock()
	paired := append([]sourceAudio(nil), mm.sourceAudios[did]...)
	mm.sourceAudioMu.Unlock()
	require.Len(t, paired, 1, "rendition pairing must be ready before distribution returns, even when HLS is blocked")
	require.NotEmpty(t, paired[0].opus)
	wantAudio := bytes.Clone(paired[0].opus)
	waitLiveWindowStack(t, "(*MediaManager).feedLiveWindow", "(*Writer).Observe")

	// Both caches must own their data after the caller reuses its segment.
	clear(segment)
	unblock()
	waitLiveWindowWork(t, playlistDone)
	require.Eventually(t, func() bool {
		return len(window.Track(trackID).Segments) > len(before.Segments)
	}, 5*time.Second, time.Millisecond)
	require.Equal(t, wantAudio, paired[0].opus)
	require.Equal(t, original, mm.BufferedSegments(did)[0].Data)
	last := window.Track(trackID)
	require.NotEmpty(t, window.SegmentData(trackID, last.Segments[len(last.Segments)-1].Seq))
}

func TestDistributionFeedsHLSAfterRequestCancellation(t *testing.T) {
	segment := sourceSegmentForTest(t)
	var presentation bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(t.Context(), bytes.NewReader(segment), "flat", &presentation))
	for _, local := range []bool{false, true} {
		name := "replicated"
		if local {
			name = "local"
		}
		t.Run(name, func(t *testing.T) {
			const did = "did:plc:cancelled-distribution"
			mm := NewOffline(&config.CLI{AllowedStreams: []string{did}, Syndicate: []string{did}})
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			require.NoError(t, mm.distributeSegment(ctx, &validatedSegment{
				repoDID: did,
				local:   local,
				meta:    &SegmentMetadata{StartTime: aqtime.FromTime(time.Now()), Published: true},
			}, segment, presentation.Bytes()))
			require.Eventually(t, func() bool {
				window, published := mm.GetLiveWindow(did)
				return window != nil && published && len(window.TrackIDs()) > 0
			}, 5*time.Second, time.Millisecond)
		})
	}
}

func BenchmarkReplicatedDistribution(b *testing.B) {
	ctx := context.Background()
	segments := allSignedBareSegments(b, ctx, newBareSegmentSigner(b), getFixture("h264-opus-frag.mp4"))
	require.NotEmpty(b, segments)
	segment := segments[0]
	var presentation bytes.Buffer
	require.NoError(b, muxl.RunMuxlWrap(ctx, bytes.NewReader(segment), "flat", &presentation))
	vs := &validatedSegment{repoDID: "did:test:distribution-benchmark", meta: &SegmentMetadata{StartTime: aqtime.FromTime(time.Now()), Published: true}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mm := NewOffline(&config.CLI{Syndicate: []string{vs.repoDID}})
		require.NoError(b, mm.distributeSegment(ctx, vs, segment, presentation.Bytes()))
		for {
			window, _ := mm.GetLiveWindow(vs.repoDID)
			if window != nil && len(window.TrackIDs()) > 0 {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
}
