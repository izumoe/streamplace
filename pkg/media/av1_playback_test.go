package media

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
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

func TestAudioCompletionKeepsSourceTimeline(t *testing.T) {
	require.NoError(t, muxl.Initialize())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	fixture, err := os.ReadFile(getFixture("av1-aac-frag.mp4"))
	require.NoError(t, err)
	canonical, err := muxl.RunMuxlCanonicalize(ctx, fixture, nil)
	require.NoError(t, err)
	events, err := unwrapMuxlEvents(ctx, canonical)
	require.NoError(t, err)
	cat := catalogFromEvents(events)
	var shifted []map[string][]byte
	for _, event := range events {
		if event.Type != "segment" && event.Type != "signed-segment" {
			continue
		}
		for _, video := range cat.Video.Renditions {
			id := strconv.FormatUint(uint64(video.TrackID()), 10)
			shiftTfdt(event.Tracks[id], int64(video.Timescale())*30)
		}
		for _, audio := range cat.Audio.Renditions {
			id := strconv.FormatUint(uint64(audio.TrackID()), 10)
			shiftTfdt(event.Tracks[id], int64(audio.Timescale())*30)
		}
		shifted = append(shifted, event.Tracks)
	}
	var input bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(concatTracksSorted(shifted...)), "fmp4", &input))
	filename := filepath.Join(t.TempDir(), "source-epoch.mp4")
	require.NoError(t, os.WriteFile(filename, input.Bytes(), 0600))
	segments := allSignedBareSegments(t, ctx, newBareSegmentSigner(t), filename)
	require.GreaterOrEqual(t, len(segments), 2)
	cert, key := nodeSignerForTest(t)
	mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "test.example.com"}}
	var completed [][]byte
	encoder := mm.newStreamTranscoder(ctx, "opus", cert, key, func(_ any, segment []byte) {
		completed = append(completed, segment)
	})
	for _, segment := range segments {
		_, err := ValidateMP4Media(ctx, segment)
		require.NoError(t, err)
		require.NoError(t, encoder.Feed(segment, nil))
	}
	require.NoError(t, encoder.Close())
	require.Len(t, completed, len(segments))
	var previousEnd float64
	for i, output := range completed {
		require.True(t, bytes.HasPrefix(output, segments[i]), "source signatures and media bytes remain unchanged")
		sourceEvents, err := unwrapMuxlEvents(ctx, segments[i])
		require.NoError(t, err)
		sourceCatalog, sourceTracks := catalogAndTracks(sourceEvents)
		var sourceStart float64
		for _, audio := range sourceCatalog.Audio.Renditions {
			base, ok := firstTfdt(sourceTracks[strconv.FormatUint(uint64(audio.TrackID()), 10)])
			require.True(t, ok)
			sourceStart = float64(base) / float64(audio.Timescale())
		}
		require.GreaterOrEqual(t, sourceStart, float64(30))
		outputEvents, err := unwrapMuxlEvents(ctx, output)
		require.NoError(t, err)
		outputCatalog, outputTracks := catalogAndTracks(outputEvents)
		var outputStart float64
		var outputTimescale uint32
		var outputTicks uint64
		for _, audio := range outputCatalog.Audio.Renditions {
			if !isOpusCodec(audio.Codec) {
				continue
			}
			base, ok := firstTfdt(outputTracks[strconv.FormatUint(uint64(audio.TrackID()), 10)])
			require.True(t, ok)
			outputTimescale = audio.Timescale()
			outputStart = float64(base) / float64(outputTimescale)
			for _, event := range outputEvents {
				outputTicks += event.Durations[strconv.FormatUint(uint64(audio.TrackID()), 10)]
			}
		}
		require.Positive(t, outputTimescale)
		source, err := ValidateMP4Media(ctx, segments[i])
		require.NoError(t, err)
		result, err := ValidateMP4Media(ctx, output)
		require.NoError(t, err, "both source and derived signatures verify after timeline alignment")
		t.Logf("batch %d source audio %.6fs, derived Opus %.6fs..%.6fs; spans %s and %s", i, sourceStart, outputStart, outputStart+float64(outputTicks)/float64(outputTimescale), source.MediaData.BatchDuration, result.MediaData.BatchDuration)
		if i > 0 {
			require.InDelta(t, previousEnd, outputStart, 1/float64(outputTimescale), "continuous audio packets must retain adjacent timestamps")
		}
		previousEnd = outputStart + float64(outputTicks)/float64(outputTimescale)
		require.Less(t, source.MediaData.BatchDuration, time.Second)
		if i == 0 {
			require.InDelta(t, sourceStart, outputStart, 1/float64(outputTimescale), "derived audio must keep the source epoch")
		}
		require.Less(t, result.MediaData.BatchDuration, time.Second, "codec packet boundaries cannot turn a short batch into its absolute timeline span")
	}
}
