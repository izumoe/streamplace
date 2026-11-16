package livehls

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/muxl"
)

func TestAudioStemAvailabilityUsesActiveGroupsRatherThanTrackOrder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		groups []uint32
		codecs []string
		want   bool
	}{
		{"codec alternatives", []uint32{1, 1}, []string{"mp4a.40.2", "opus"}, false},
		{"higher Opus group", []uint32{3, 1}, []string{"opus", "mp4a.40.2"}, true},
		{"AAC-only extra group", []uint32{1, 2}, []string{"opus", "mp4a.40.2"}, false},
		{"ungrouped alternatives", []uint32{0, 0}, []string{"opus", "mp4a.40.2"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := NewWriter()
			audio := map[string]muxl.MuxlAudioConfig{}
			for i, group := range tc.groups {
				id := uint32(i + 1)
				audio[tc.codecs[i]] = muxl.MuxlAudioConfig{Codec: tc.codecs[i], Container: muxl.MuxlContainer{Kind: "cmaf", TrackID: id, Timescale: 48000}, AlternateGroup: group}
			}
			require.NoError(t, writer.Observe(&muxl.MuxlEvent{Type: "init", Catalog: &muxl.MuxlCatalog{Audio: &muxl.MuxlCatalogAudio{Renditions: audio}}}))
			require.False(t, writer.HasAudioStems(), "a catalog alone is not live audio")
			require.NoError(t, writer.Observe(&muxl.MuxlEvent{Type: "segment", Tracks: map[string][]byte{"1": {1}, "2": {2}}, Durations: map[string]uint64{"1": 48000, "2": 48000}}))
			require.Equal(t, tc.want, writer.HasAudioStems())
			writer.Finalize()
			require.False(t, writer.HasAudioStems(), "a finalized window is not live")
		})
	}
}

func TestAudioStemAvailabilityExpiresWithoutMoreInput(t *testing.T) {
	now := time.Now()
	writer := NewWriter(WithRetention(time.Second))
	writer.now = func() time.Time { return now }
	require.NoError(t, writer.Observe(&muxl.MuxlEvent{Type: "init", Catalog: &muxl.MuxlCatalog{Audio: &muxl.MuxlCatalogAudio{Renditions: map[string]muxl.MuxlAudioConfig{
		"main": {Codec: "opus", Container: muxl.MuxlContainer{Kind: "cmaf", TrackID: 1, Timescale: 48000}, AlternateGroup: 1},
		"stem": {Codec: "opus", Container: muxl.MuxlContainer{Kind: "cmaf", TrackID: 2, Timescale: 48000}, AlternateGroup: 2},
	}}}}))
	require.NoError(t, writer.Observe(&muxl.MuxlEvent{Type: "segment", Tracks: map[string][]byte{"1": {1}, "2": {2}}}))
	require.True(t, writer.HasAudioStems())
	now = now.Add(2 * time.Second)
	require.NoError(t, writer.Observe(&muxl.MuxlEvent{Type: "segment", Tracks: map[string][]byte{"1": {1}}}))
	require.False(t, writer.HasAudioStems(), "old stem data must not keep controls available while main continues")
}
