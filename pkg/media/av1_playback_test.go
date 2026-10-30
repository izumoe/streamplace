package media

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/muxl"
)

func TestAV1AudioCompletionPreservesSignedVideo(t *testing.T) {
	// The deadline measures media processing, after cold WASM compilation.
	require.NoError(t, muxl.Initialize())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ms := newBareSegmentSigner(t)
	segments := allSignedBareSegments(t, ctx, ms, getFixture("av1-aac-frag.mp4"))
	require.GreaterOrEqual(t, len(segments), 2)
	cert, key := nodeSignerForTest(t)
	mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "test.example.com"}}
	var mu sync.Mutex
	var completed [][]byte
	encoder := mm.newStreamTranscoder(ctx, "opus", cert, key, func(_ any, segment []byte) {
		mu.Lock()
		completed = append(completed, segment)
		mu.Unlock()
	})
	for _, segment := range segments {
		require.NoError(t, encoder.Feed(segment, nil))
	}
	require.NoError(t, encoder.Close())
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, completed, len(segments), "every source GoP receives its matching Opus audio")
	for i, segment := range completed {
		require.True(t, bytes.HasPrefix(segment, segments[i]), "original signed AV1 and AAC bytes remain intact")
		codecs := audioCodecsOf(t, t.Context(), segment)
		require.Len(t, codecs, 2)
		var opus, aac bool
		for _, codec := range codecs {
			opus = opus || isOpusCodec(codec)
			aac = aac || isAACCodec(codec)
		}
		require.True(t, opus && aac)
		_, err := ValidateMP4MediaC2PA(t.Context(), segment)
		require.NoError(t, err, "both source and derived track signatures validate")
	}
}

func TestAV1SourcePublishesWithoutH264Packetization(t *testing.T) {
	streamer, source := workerSourceForTest(t, "av1-aac-frag.mp4")
	mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "node.test", AllowedStreams: []string{streamer}}, bus: bus.NewBus()}
	view := mm.bus.SubscribeSegment(t.Context(), streamer, PlaybackSourceRendition)
	defer mm.bus.UnsubscribeSegment(t.Context(), streamer, PlaybackSourceRendition, view)
	webrtc := mm.bus.SubscribeSegment(t.Context(), streamer, WebRTCSourceRendition)
	defer mm.bus.UnsubscribeSegment(t.Context(), streamer, WebRTCSourceRendition, webrtc)
	segment, err := mm.consumeSourcePhase(t.Context(), source, streamer, true)
	require.NoError(t, err)
	require.NotNil(t, segment)
	require.Equal(t, source, segment.Muxl, "native MoQ receives the exact verified AV1 source")
	require.True(t, segment.Published)
	require.Positive(t, segment.BatchDuration, "MoQ timing is measured without H264 packetization")
	require.Nil(t, segment.PacketizedData)
	require.Len(t, view.C, 1)
	require.Same(t, segment, <-view.C)
	require.Empty(t, webrtc.C, "AV1 and AAC do not enter the H264/Opus packet channel")
}
