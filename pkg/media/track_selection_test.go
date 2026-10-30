package media

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/muxl"
)

func TestSelectSegmentTracksPreservesExactVideo(t *testing.T) {
	ctx := context.Background()
	fixture, err := os.ReadFile(getFixture("h264-opus-frag.mp4"))
	require.NoError(t, err)
	base, err := muxl.RunMuxlCanonicalize(ctx, fixture, map[uint32]uint32{1: 7, 2: 42})
	require.NoError(t, err)
	events, err := unwrapMuxlEvents(ctx, base)
	require.NoError(t, err)
	_, tracks := catalogAndTracks(events)
	other, err := muxl.RunMuxlCanonicalize(ctx, renditionMP4(t, 160, 120), map[uint32]uint32{1: 20})
	require.NoError(t, err)
	otherEvents, err := unwrapMuxlEvents(ctx, other)
	require.NoError(t, err)
	_, extra := catalogAndTracks(otherEvents)
	tracks["20"] = extra["20"]
	source := concatTracksByID(tracks)
	for _, id := range []uint32{7, 20} {
		selected, err := selectSegmentTracks(ctx, source, id, "avc1", true)
		require.NoError(t, err)
		got, err := unwrapMuxlEvents(ctx, selected)
		require.NoError(t, err)
		cat, actual := catalogAndTracks(got)
		require.Len(t, cat.Video.Renditions, 1)
		for _, video := range cat.Video.Renditions {
			require.Equal(t, id, video.TrackID(), "equal-height tracks retain their exact identity")
		}
		for key, data := range actual {
			require.True(t, bytes.Equal(tracks[key], data), "selection preserves signed track bytes")
		}
		require.Contains(t, actual, "42")
	}
	_, err = selectSegmentTracks(ctx, source, 99, "avc1", true)
	require.ErrorContains(t, err, "video track")
}

func TestThumbnailAV1Source(t *testing.T) {
	fixture, err := os.ReadFile(getFixture("av1-aac-frag.mp4"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var thumb bytes.Buffer
	require.NoError(t, Thumbnail(ctx, bytes.NewReader(fixture), &thumb, "jpeg"))
	require.True(t, bytes.HasPrefix(thumb.Bytes(), []byte{0xff, 0xd8}))
}

func TestPrimaryVideoTrackDeterministic(t *testing.T) {
	cat := &muxl.MuxlCatalog{Video: &muxl.MuxlCatalogVideo{Renditions: map[string]muxl.MuxlVideoConfig{
		"av1": {Codec: "av01.0.04M.08", CodedWidth: 1920, CodedHeight: 1080, Container: muxl.MuxlContainer{TrackID: 9}},
		"a":   {Codec: "avc1.64001f", CodedWidth: 1280, CodedHeight: 720, Container: muxl.MuxlContainer{TrackID: 7}},
		"b":   {Codec: "avc1.64001f", CodedWidth: 1280, CodedHeight: 720, Container: muxl.MuxlContainer{TrackID: 2}},
	}}}
	for range 20 {
		video, ok := primaryVideoTrack(cat, 0, "")
		require.True(t, ok)
		require.Equal(t, uint32(9), video.TrackID())
		video, ok = primaryVideoTrack(cat, 0, "avc1")
		require.True(t, ok)
		require.Equal(t, uint32(2), video.TrackID())
		_, ok = primaryVideoTrack(cat, 9, "avc1")
		require.False(t, ok, "an incompatible explicit track must not silently select another")
	}
}
