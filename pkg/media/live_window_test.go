package media

import (
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/livehls"
)

func waitLiveWindowWork(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("live window work did not reach the expected synchronization point")
	}
}

func waitLiveWindowStack(t *testing.T, caller, callee string) {
	t.Helper()
	require.Eventually(t, func() bool {
		stack := make([]byte, 64<<10)
		n := runtime.Stack(stack, true)
		for _, goroutine := range strings.Split(string(stack[:n]), "\n\n") {
			if strings.Contains(goroutine, caller) && strings.Contains(goroutine, callee) {
				return true
			}
		}
		return false
	}, 5*time.Second, time.Millisecond)
}

func TestFeedLiveWindowPublicationSnapshots(t *testing.T) {
	ctx := t.Context()
	segments := allSignedBareSegments(t, ctx, newBareSegmentSigner(t), getFixture("h264-opus-frag.mp4"))
	require.GreaterOrEqual(t, len(segments), 2)
	for _, initiallyPublished := range []bool{false, true} {
		name := "preview to public"
		if initiallyPublished {
			name = "public to preview"
		}
		t.Run(name, func(t *testing.T) {
			const did = "did:test:publication"
			mm := NewOffline(&config.CLI{})
			started := time.Now()
			mm.feedLiveWindow(ctx, did, segments[0], started, initiallyPublished)
			captured, published := mm.GetLiveWindow(did)
			require.NotNil(t, captured)
			require.Equal(t, initiallyPublished, published)
			trackID := captured.TrackIDs()[0]
			before := captured.Track(trackID)
			require.NotEmpty(t, before.Segments)
			data := captured.SegmentData(trackID, before.Segments[0].Seq)

			mm.feedLiveWindow(ctx, did, segments[1], started.Add(time.Second), !initiallyPublished)
			current, published := mm.GetLiveWindow(did)
			require.NotNil(t, current)
			require.Equal(t, !initiallyPublished, published)
			require.NotSame(t, captured, current, "a policy change must not append media to an earlier request's writer")
			require.Equal(t, before, captured.Track(trackID), "captured window keeps the segments admitted under its policy")
			require.Equal(t, data, captured.SegmentData(trackID, before.Segments[0].Seq))
		})
	}
}

func TestFeedLiveWindowDoesNotBlockOtherStreams(t *testing.T) {
	ctx := t.Context()
	ms := newBareSegmentSigner(t)
	segments := allSignedBareSegments(t, ctx, ms, getFixture("h264-opus-frag.mp4"))
	require.NotEmpty(t, segments)
	mm := NewOffline(&config.CLI{})
	mm.feedLiveWindow(ctx, "did:test:busy", segments[0], time.Now(), true)
	window, _ := mm.GetLiveWindow("did:test:busy")
	require.NotNil(t, window)

	entered := make(chan struct{})
	release := make(chan struct{})
	playlistDone := make(chan struct{})
	go func() {
		defer close(playlistDone)
		first := true
		window.MasterPlaylist(func(tid string) string {
			if first {
				first = false
				close(entered)
				<-release
			}
			return tid + ".m3u8"
		})
	}()
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() {
		unblock()
		waitLiveWindowWork(t, playlistDone)
	})
	waitLiveWindowWork(t, entered)
	feedDone := make(chan struct{})
	go func() {
		defer close(feedDone)
		mm.feedLiveWindow(ctx, "did:test:busy", segments[0], time.Now(), true)
	}()
	t.Cleanup(func() {
		unblock()
		waitLiveWindowWork(t, feedDone)
	})
	otherDone := make(chan struct{})

	// Wait for the feed to reach the writer held by the playlist callback,
	// rather than depending on how long decoding or goroutine scheduling takes.
	waitLiveWindowStack(t, "(*MediaManager).feedLiveWindow", "(*Writer).Observe")
	go func() {
		defer close(otherDone)
		mm.feedLiveWindow(ctx, "did:test:other", segments[0], time.Now(), true)
		mm.GetLiveWindow("did:test:other")
	}()
	t.Cleanup(func() {
		unblock()
		waitLiveWindowWork(t, otherDone)
	})
	select {
	case <-otherDone:
	case <-time.After(time.Second):
		t.Error("unrelated stream feed and reads blocked by busy stream")
	}
}

func TestFeedLiveWindowRecreatesPrunedState(t *testing.T) {
	ctx := t.Context()
	ms := newBareSegmentSigner(t)
	segments := allSignedBareSegments(t, ctx, ms, getFixture("h264-opus-frag.mp4"))
	require.NotEmpty(t, segments)
	const did = "did:test:pruned"
	mm := NewOffline(&config.CLI{})
	mm.feedLiveWindow(ctx, did, segments[0], time.Now(), true)
	window, _ := mm.GetLiveWindow(did)
	require.NotNil(t, window)
	entered := make(chan struct{})
	release := make(chan struct{})
	playlistDone := make(chan struct{})
	go func() {
		defer close(playlistDone)
		first := true
		window.MasterPlaylist(func(tid string) string {
			if first {
				first = false
				// The playlist owns the writer lock; expire its segments before
				// the queued read can check whether the window is empty.
				livehls.WithRetention(time.Nanosecond)(window)
				close(entered)
				<-release
			}
			return tid + ".m3u8"
		})
	}()
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() {
		unblock()
		waitLiveWindowWork(t, playlistDone)
	})
	waitLiveWindowWork(t, entered)
	readDone := make(chan struct{})
	var readWindow *livehls.Writer
	go func() {
		defer close(readDone)
		readWindow, _ = mm.GetLiveWindow(did)
	}()
	t.Cleanup(func() {
		unblock()
		waitLiveWindowWork(t, readDone)
	})
	waitLiveWindowStack(t, "(*MediaManager).GetLiveWindow", "(*Writer).Empty")
	feedDone := make(chan struct{})
	go func() {
		defer close(feedDone)
		mm.feedLiveWindow(ctx, did, segments[0], time.Now(), true)
	}()
	t.Cleanup(func() {
		unblock()
		waitLiveWindowWork(t, feedDone)
	})
	waitLiveWindowStack(t, "(*MediaManager).feedLiveWindow", "sync.(*Mutex).Lock")
	unblock()
	waitLiveWindowWork(t, playlistDone)
	waitLiveWindowWork(t, feedDone)
	waitLiveWindowWork(t, readDone)
	require.Nil(t, readWindow)
	recreated, published := mm.GetLiveWindow(did)
	require.NotNil(t, recreated, "queued feed must populate the current state, not the pruned one")
	require.NotSame(t, window, recreated)
	require.True(t, published)
	require.Contains(t, recreated.MasterPlaylist(func(tid string) string { return tid + ".m3u8" }), "opus")
}
