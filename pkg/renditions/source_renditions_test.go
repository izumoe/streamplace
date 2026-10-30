package renditions

import (
	"testing"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/placestream"
)

func TestAV1CompatibilityProfilesUseLargestSource(t *testing.T) {
	segment := &placestream.Segment{Video: []placestream.Segment_Video{
		{Codec: "av1", Width: 160, Height: 120},
		{Codec: "av1", Width: 1920, Height: 1080},
	}}
	got, err := GenerateRenditionsFrom(segment, DesiredRenditions)
	require.NoError(t, err)
	require.Greater(t, len(got), 1)
	require.Equal(t, int64(1280), got[0].Width)
	require.Equal(t, int64(720), got[0].Height)
	segment.Video[0], segment.Video[1] = segment.Video[1], segment.Video[0]
	again, err := GenerateRenditionsFrom(segment, DesiredRenditions)
	require.NoError(t, err)
	require.Equal(t, got, again)
}

func TestPublisherLadderAvoidsVideoEncoding(t *testing.T) {
	id1, id2 := "1", "2"
	segment := &placestream.Segment{Video: []placestream.Segment_Video{
		{TrackId: &id1, Codec: "av1", Width: 1920, Height: 1080},
		{TrackId: &id2, Codec: "h264", Width: 640, Height: 360},
	}}
	got, err := GenerateRenditionsFrom(segment, DesiredRenditions)
	require.NoError(t, err)
	require.Empty(t, got, "a publisher-provided compatible ladder needs no server video encoding")
}

func TestSmallAV1SourceGetsCompatibleProfile(t *testing.T) {
	segment := &placestream.Segment{Video: []placestream.Segment_Video{{Codec: "av1", Width: 160, Height: 120}}}
	got, err := GenerateRenditionsFrom(segment, DesiredRenditions)
	require.NoError(t, err)
	require.Len(t, got, 1, "a source below the configured ladder still needs an H264 compatibility rendition")
	require.Equal(t, int64(160), got[0].Width)
	require.Equal(t, int64(120), got[0].Height)
	require.Equal(t, "baseline", got[0].Profile)
}
