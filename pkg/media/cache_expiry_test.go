package media

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/livehls"
	"stream.place/streamplace/pkg/muxl"
)

func TestMediaCacheExpiryWithoutActivity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mm := &MediaManager{}
		defer startMediaCacheExpiry(mm)()
		mm.bufferSegment("did:clip", time.Now(), true, true, []byte("clip"))
		mm.RetainSegments("did:recorded", 12*time.Minute)
		mm.bufferSegment("did:recorded", time.Now(), true, true, []byte("recorded"))

		time.Sleep(segmentBufferWindow - time.Second)
		synctest.Wait()
		mm.segBuffersMut.Lock()
		var clip []byte
		if buffer := mm.segBuffers["did:clip"]; buffer != nil && len(buffer.segs) > 0 {
			clip = buffer.segs[0].Data
		}
		mm.segBuffersMut.Unlock()
		require.Equal(t, []byte("clip"), clip)

		time.Sleep(10 * time.Second)
		synctest.Wait()
		mm.segBuffersMut.Lock()
		_, clipExists := mm.segBuffers["did:clip"]
		recorded := mm.segBuffers["did:recorded"]
		mm.segBuffersMut.Unlock()
		require.False(t, clipExists, "expired bytes must be released without another segment or clip read")
		require.NotNil(t, recorded, "recording retention must survive the default clip window")
		require.Equal(t, []byte("recorded"), recorded.segs[0].Data)

		time.Sleep(10 * time.Minute)
		synctest.Wait()
		mm.segBuffersMut.Lock()
		_, recordedExists := mm.segBuffers["did:recorded"]
		mm.segBuffersMut.Unlock()
		require.False(t, recordedExists, "recording bytes must expire after their extended window")
	})
}

func TestMediaCacheExpiryLiveWindowWithoutReaders(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mm := &MediaManager{liveWindows: map[string]*liveWindowState{}}
		defer startMediaCacheExpiry(mm)()
		writer := newLiveWindow()
		require.NoError(t, writer.Observe(&muxl.MuxlEvent{Type: "segment", Tracks: map[string][]byte{"1": []byte("video")}}))
		mm.liveWindows["did:video"] = &liveWindowState{writer: writer}

		time.Sleep(liveWindowRetention - time.Second)
		synctest.Wait()
		mm.liveWindowsMut.Lock()
		_, before := mm.liveWindows["did:video"]
		mm.liveWindowsMut.Unlock()
		require.True(t, before, "the HLS grace window must remain available")

		time.Sleep(10 * time.Second)
		synctest.Wait()
		mm.liveWindowsMut.Lock()
		_, after := mm.liveWindows["did:video"]
		mm.liveWindowsMut.Unlock()
		require.False(t, after, "ended HLS bytes must expire without another playlist request")
	})
}

func startMediaCacheExpiry(mm *MediaManager) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mm.runCacheExpiry(ctx)
		close(done)
	}()
	synctest.Wait()
	return func() {
		cancel()
		<-done
	}
}

func TestMediaCacheExpiryCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mm := &MediaManager{}
		stop := startMediaCacheExpiry(mm)
		mm.bufferSegment("did:clip", time.Now(), true, true, []byte("clip"))
		stop()

		time.Sleep(segmentBufferWindow + time.Minute)
		synctest.Wait()
		mm.segBuffersMut.Lock()
		_, exists := mm.segBuffers["did:clip"]
		mm.segBuffersMut.Unlock()
		require.True(t, exists, "node cancellation must stop background cache work")
	})
}

func TestMediaCacheExpiryPreservesConcurrentReplacement(t *testing.T) {
	mm := &MediaManager{liveWindows: map[string]*liveWindowState{}}
	const did = "did:video"
	old := newLiveWindow()
	require.NoError(t, old.Observe(&muxl.MuxlEvent{Type: "segment", Tracks: map[string][]byte{"1": []byte("old")}}))
	mm.liveWindows[did] = &liveWindowState{writer: old}

	ctx, cancel := context.WithCancel(t.Context())
	cleanerDone := make(chan struct{})
	defer func() {
		cancel()
		<-cleanerDone
	}()
	entered := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	playlistDone := make(chan struct{})
	go func() {
		defer close(playlistDone)
		old.MediaPlaylist("1", "init", func(uint64) string {
			livehls.WithRetention(time.Nanosecond)(old)
			close(entered)
			<-release
			return "segment"
		})
	}()
	<-entered
	go func() {
		mm.runCacheExpiry(ctx)
		close(cleanerDone)
	}()
	// Mutex waits do not let synctest advance its clock. Wait for the real
	// maintenance tick, then prove the cleaner reached the held writer.
	time.Sleep(5 * time.Second)
	waitLiveWindowStack(t, "(*MediaManager).runCacheExpiry", "(*Writer).Empty")

	fresh := newLiveWindow()
	require.NoError(t, fresh.Observe(&muxl.MuxlEvent{Type: "segment", Tracks: map[string][]byte{"1": []byte("fresh")}}))
	feedDone := make(chan struct{})
	go func() {
		defer close(feedDone)
		state := mm.lockLiveWindow(did, true)
		state.writer = fresh
		state.mu.Unlock()
	}()
	waitLiveWindowStack(t, "(*MediaManager).lockLiveWindow", "sync.(*Mutex).Lock")
	unblock()
	waitLiveWindowWork(t, playlistDone)
	waitLiveWindowWork(t, feedDone)
	mm.liveWindowsMut.Lock()
	current := mm.liveWindows[did]
	mm.liveWindowsMut.Unlock()
	require.NotNil(t, current)
	require.Same(t, fresh, current.writer, "expiry of the old writer must not erase the concurrent feed")
}
