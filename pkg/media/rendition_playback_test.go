package media

import (
	"context"
	"runtime"
	"strconv"
	"testing"
	"time"
	"weak"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
)

func TestSourceAudioReleasesEvictedPayloads(t *testing.T) {
	ctx := t.Context()
	source := sourceSegmentForTest(t)
	mm := &MediaManager{}
	const did = "did:test:audio-cache"
	var keys []uint64
	var timescale uint32
	released := func() weak.Pointer[byte] {
		var ref weak.Pointer[byte]
		for i := range sourceAudioKeep + 1 {
			events, err := unwrapMuxlEvents(ctx, source)
			require.NoError(t, err)
			catalog, tracks := catalogAndTracks(events)
			require.NotNil(t, catalog)
			video, ok := primaryVideoTrack(catalog, 0, "")
			require.True(t, ok)
			timescale = video.Timescale()
			require.Positive(t, timescale)
			track := tracks[strconv.FormatUint(uint64(video.TrackID()), 10)]
			shiftTfdt(track, int64(i)*int64(timescale))
			key, ok := firstTfdt(track)
			require.True(t, ok)
			keys = append(keys, key)
			mm.rememberSourceAudio(did, strconv.Itoa(i), events)
			if i == 0 {
				payload := mm.sourceAudios[did][0].opus
				require.NotEmpty(t, payload)
				ref = weak.Make(&payload[0])
			}
		}
		return ref
	}()
	_, found := mm.sourceAudioAt(did, keys[0], timescale)
	require.False(t, found)
	for _, key := range keys[1:] {
		entry, found := mm.sourceAudioAt(did, key, timescale)
		require.True(t, found)
		require.NotEmpty(t, entry.opus)
	}
	runtime.GC()
	runtime.GC()
	collected := released.Value() == nil
	runtime.KeepAlive(mm)
	require.True(t, collected, "evicted audio must be collectible while the pairing cache remains alive")
}

// A node that receives a stream's rendition addendum (a syndicating node)
// packetizes each rendition with the source segment's Opus audio and
// publishes it on its bus under the rendition's name, so WebRTC viewers
// there can pick it.
func TestPublishRenditionsForPlayback(t *testing.T) {
	ctx := context.Background()
	src := sourceSegmentForTest(t)
	r160 := renditionMP4(t, 160, 120)
	cert, keyPEM := nodeSignerForTest(t)
	b := bus.NewBus()
	mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "node.test"}, liveWindows: map[string]*liveWindowState{}, bus: b}
	addendum, err := mm.mintVideoRenditions(ctx, src, []RenditionInput{{Name: "120p", MP4: r160}}, cert, keyPEM)
	require.NoError(t, err)

	const did = "did:test:streamer"
	sub := b.SubscribeSegment(ctx, did, "120p")
	defer b.UnsubscribeSegment(ctx, did, "120p", sub)
	compatible := b.SubscribeSegment(ctx, did, WebRTCSourceRendition)
	defer b.UnsubscribeSegment(ctx, did, WebRTCSourceRendition, compatible)

	// Without the source segment's audio there is nothing to pair with.
	require.ErrorIs(t, mm.PublishRenditionsForPlayback(ctx, did, addendum, true), ErrAudioSourceMissing)
	require.Empty(t, sub.C)

	events, err := unwrapMuxlEvents(ctx, src)
	require.NoError(t, err)
	valid, err := ValidateMP4MediaC2PA(ctx, src)
	require.NoError(t, err)
	mm.bufferSegment(did, valid.Meta.StartTime.Time(), true, false, src, 0, *valid.Manifest.Label)
	require.NoError(t, mm.FeedLiveRenditions(ctx, did, src, valid.Meta.StartTime.Time(), true))
	mm.rememberSourceAudio(did, *valid.Manifest.Label, events)
	want, err := Packetize(ctx, mm.cli, &bus.Seg{Data: r160}, 0)
	require.NoError(t, err)
	require.NoError(t, mm.PublishRenditionsForPlayback(ctx, did, addendum, true))
	select {
	case seg := <-sub.C:
		require.True(t, seg.Published)
		require.NotNil(t, seg.PacketizedData)
		require.NotEmpty(t, seg.PacketizedData.Video, "video samples")
		require.Len(t, seg.PacketizedData.Video, len(want.Video), "every rendition fragment reaches peer playback")
		require.NotEmpty(t, seg.PacketizedData.Audio, "audio samples")
		require.Greater(t, seg.PacketizedData.Duration, time.Duration(0))
	case <-time.After(10 * time.Second):
		t.Fatal("no rendition segment published")
	}
	require.Empty(t, compatible.C, "an H264 publisher already provides its default playback source")
}

func TestReplicatedAV1RenditionProvidesWebRTCSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	ms := newBareSegmentSigner(t)
	sources := allSignedBareSegments(t, ctx, ms, getFixture("av1-aac-frag.mp4"))
	cert, nodeKey := nodeSignerForTest(t)
	b := bus.NewBus()
	mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "node.test"}, bus: b}
	var completed [][]byte
	encoder := mm.newStreamTranscoder(ctx, "opus", cert, nodeKey, func(_ any, segment []byte, audioOnly bool) { appendAudioCompletion(&completed, segment, audioOnly) })
	for _, source := range sources {
		require.NoError(t, encoder.Feed(source, nil))
	}
	require.NoError(t, encoder.Close())
	require.Len(t, completed, len(sources))
	require.GreaterOrEqual(t, len(completed), 2)
	addendum, err := mm.mintVideoRenditions(ctx, completed[1], []RenditionInput{{Name: "120p", MP4: renditionMP4(t, 160, 120)}}, cert, nodeKey)
	require.NoError(t, err)
	const did = "did:example:replicated-av1"
	compatible := b.SubscribeSegment(ctx, did, WebRTCSourceRendition)
	defer b.UnsubscribeSegment(ctx, did, WebRTCSourceRendition, compatible)
	named := b.SubscribeSegment(ctx, did, "120p")
	defer b.UnsubscribeSegment(ctx, did, "120p", named)
	events, err := unwrapMuxlEvents(ctx, completed[1])
	require.NoError(t, err)
	valid, err := ValidateMP4MediaC2PA(ctx, completed[1])
	require.NoError(t, err)
	mm.bufferSegment(did, valid.Meta.StartTime.Time(), true, false, completed[1], 0, *valid.Manifest.Label)
	require.NoError(t, mm.FeedLiveRenditions(ctx, did, completed[1], valid.Meta.StartTime.Time(), true))
	mm.rememberSourceAudio(did, *valid.Manifest.Label, events)
	require.NoError(t, mm.PublishRenditionsForPlayback(ctx, did, addendum, true))
	require.Len(t, named.C, 1, "nonzero decode times pair correctly across source and rendition timescales")
	require.Len(t, compatible.C, 1, "a peer uses the H264 addendum for default WHEP when its source is AV1")
	segment := <-compatible.C
	require.True(t, segment.Published)
	require.NotEmpty(t, segment.PacketizedData.Video)
	require.NotEmpty(t, segment.PacketizedData.Audio)
}
