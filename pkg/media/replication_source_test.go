package media

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/muxl"
)

func TestH264ReplicationSourcePreservesSignedTracksAndAudioTail(t *testing.T) {
	ctx := t.Context()
	fixture, err := os.ReadFile(getFixture("av1-aac-frag.mp4"))
	require.NoError(t, err)
	canonical, err := muxl.RunMuxlCanonicalize(ctx, fixture, map[uint32]uint32{1: 1, 2: 5})
	require.NoError(t, err)
	events, err := unwrapMuxlEvents(ctx, canonical)
	require.NoError(t, err)
	_, tracks := catalogAndTracks(events)
	for i, size := range [][2]int{{240, 180}, {160, 120}, {80, 60}} {
		id := uint32(i + 2)
		if i > 0 {
			id |= 1 << 16
		}
		canonical, err := muxl.RunMuxlCanonicalize(ctx, renditionMP4(t, size[0], size[1]), map[uint32]uint32{1: id})
		require.NoError(t, err)
		events, err := unwrapMuxlEvents(ctx, canonical)
		require.NoError(t, err)
		_, video := catalogAndTracks(events)
		tracks[fmt.Sprint(id)] = video[fmt.Sprint(id)]
	}
	var input bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(concatTracksByID(tracks)), "fmp4", &input))
	filename := filepath.Join(t.TempDir(), "mixed.mp4")
	require.NoError(t, os.WriteFile(filename, input.Bytes(), 0600))

	for _, public := range []bool{true, false} {
		t.Run(fmt.Sprintf("public=%t", public), func(t *testing.T) {
			signer := audioSignerForTest(t)
			signer.PrebuiltManifest = audioPolicyManifest(t, signer.PrebuiltManifest, public, "CC-BY-4.0")
			// One completion batch can contain multiple original signed GoPs.
			// Every input track must retain its own original C2PA asset.
			source := bytes.Join(allSignedBareSegments(t, ctx, signer, filename), nil)
			input, err := signer.SigningInput()
			require.NoError(t, err)
			encoder := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
			var completed, tail []byte
			tr := encoder.newStreamTranscoder(ctx, "opus", input.CertPEM, input.KeyPEM, func(_ any, data []byte, audioOnly bool) {
				if audioOnly {
					tail = append(tail, data...)
				} else {
					completed = append(completed, data...)
				}
			})
			require.NoError(t, tr.Feed(source, nil))
			require.NoError(t, tr.Close())
			require.NotEmpty(t, tail)
			full, err := unwrapMuxlEvents(ctx, completed)
			require.NoError(t, err)
			fullCat, fullTracks := catalogAndTracks(full)
			require.Len(t, fullCat.Video.Renditions, 4)
			require.Len(t, fullCat.Audio.Renditions, 2)

			selected, addendum, err := H264ReplicationSource(ctx, completed)
			require.NoError(t, err)
			extra, err := MuxlTracks(ctx, addendum)
			require.NoError(t, err)
			require.Len(t, extra, 2, "all remaining AVC renditions are sent without AV1 or duplicate audio")
			for _, id := range []string{"65539", "65540"} {
				require.Equal(t, fullTracks[id], extra["video"+id], "additional renditions retain their signed track IDs and bytes")
			}
			_, err = ValidateMP4MediaC2PA(ctx, addendum)
			require.NoError(t, err, "the original rendition signatures verify without re-signing")
			selectedEvents, err := unwrapMuxlEvents(ctx, selected)
			require.NoError(t, err)
			cat, actual := catalogAndTracks(selectedEvents)
			require.Len(t, cat.Video.Renditions, 1)
			require.Len(t, cat.Audio.Renditions, 2, "legacy peers need completed Opus before video addenda arrive")
			require.Len(t, actual, 3)
			for _, video := range cat.Video.Renditions {
				require.Equal(t, uint32(2), video.TrackID())
				require.Equal(t, uint32(240), video.CodedWidth)
				require.Equal(t, uint32(180), video.CodedHeight)
			}
			for id, track := range actual {
				require.Equal(t, fullTracks[id], track, "track %s's signed bytes must not be rewritten", id)
			}
			original, err := ValidateMP4MediaC2PA(ctx, completed)
			require.NoError(t, err)
			valid, err := ValidateMP4MediaC2PA(ctx, selected)
			require.NoError(t, err, "selected source and derivative signatures still verify")
			selectedVideo, err := ValidateMP4MediaC2PA(ctx, fullTracks["2"])
			require.NoError(t, err)
			require.Equal(t, selectedVideo.Meta, valid.Meta, "retain the selected asset's signed timestamp and policy")
			require.Equal(t, *selectedVideo.Manifest.Label, *valid.Manifest.Label)
			require.Equal(t, public, valid.Meta.Published)
			require.NotEqual(t, *original.Manifest.Label, *valid.Manifest.Label, "omitting AV1 changes the primary asset, not its signed identity")
			single, extraVideo, err := H264ReplicationSource(ctx, selected)
			require.NoError(t, err)
			require.Equal(t, selected, single)
			require.Empty(t, extraVideo, "a single-video source does not create a duplicate rendition")

			did := signer.Streamer()
			legacy := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
			legacy.bufferSegment(did, valid.Meta.StartTime.Time(), public, false, selected, 0, *valid.Manifest.Label)
			require.NoError(t, legacy.ReceiveAudioAddendum(ctx, did, tail), "selected Opus retains its valid continuation")

			mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
			mm.bufferSegment(did, original.Meta.StartTime.Time(), public, false, completed, 0, *original.Manifest.Label)
			notifications := mm.NewSegment()
			require.NoError(t, mm.ReceiveAudioAddendum(ctx, did, tail), "full source peers retain the completed codec's EOS tail")
			select {
			case notification := <-notifications:
				require.True(t, notification.AudioOnly)
				require.Equal(t, *original.Manifest.Label, notification.Segment.ID)
				require.Equal(t, public, notification.Metadata.Published)
				require.Equal(t, tail, notification.Muxl)
			case <-time.After(time.Second):
				t.Fatal("the projected source's EOS tail was not queued")
			}
			require.Equal(t, append(bytes.Clone(completed), tail...), mm.BufferedSegments(did)[0].Data)
			require.Error(t, mm.ReceiveAudioAddendum(ctx, did, tail), "tail replay must not duplicate encoded samples")
		})
	}
}

func TestH264ReplicationSourceRejectsAV1Only(t *testing.T) {
	sources := allSignedBareSegments(t, t.Context(), audioSignerForTest(t), getFixture("av1-aac-frag.mp4"))
	require.NotEmpty(t, sources)
	selected, addendum, err := H264ReplicationSource(t.Context(), sources[0])
	require.Error(t, err)
	require.Empty(t, selected, "an incompatible source must not be advertised as H264")
	require.Empty(t, addendum)
}

func TestH264ReplicationSourceKeepsOriginalAudio(t *testing.T) {
	for _, tc := range []struct {
		fixture, target string
		streamSigned    bool
	}{
		{"ertmp-red-h264.mp4", "opus", false},
		{"ertmp-red-h264.mp4", "opus", true},
		{"h264-opus-frag.mp4", "aac", false},
	} {
		t.Run(fmt.Sprintf("%s/streamSigned=%t", tc.target, tc.streamSigned), func(t *testing.T) {
			ctx := t.Context()
			signer := audioSignerForTest(t)
			source := allSignedBareSegments(t, ctx, signer, getFixture(tc.fixture))[0]
			expected, err := MuxlTracks(ctx, source)
			require.NoError(t, err)
			cert, key := nodeSignerForTest(t)
			encoder := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
			var completed []byte
			tr := encoder.newStreamTranscoder(ctx, tc.target, cert, key, func(_ any, data []byte, tail bool) {
				if !tail {
					completed = append(completed, data...)
				}
			})
			if tc.streamSigned {
				tr.signer, err = signer.SigningInput()
				require.NoError(t, err)
			}
			require.NoError(t, tr.Feed(source, nil))
			require.NoError(t, tr.Close())
			full, err := MuxlTracks(ctx, completed)
			require.NoError(t, err)
			require.Len(t, full, len(expected)+1, "local playback retains codec completion")
			selected, extra, err := H264ReplicationSource(ctx, completed)
			require.NoError(t, err)
			require.Empty(t, extra)
			got, err := MuxlTracks(ctx, selected)
			require.NoError(t, err)
			if tc.target == "opus" && tc.streamSigned {
				require.Equal(t, len(expected)+1, len(got), "legacy peers receive completed Opus without encoding it again")
				for name, track := range full {
					if len(expected[name]) == 0 {
						expected[name] = track
					}
				}
			}
			require.True(t, maps.EqualFunc(expected, got, bytes.Equal), "legacy peers retain original signed tracks and completed Opus")
			_, err = ValidateMP4MediaC2PA(ctx, selected)
			require.NoError(t, err)
			named, renditions, err := H264ReplicationTracks(ctx, completed, 0)
			require.NoError(t, err)
			require.True(t, maps.EqualFunc(expected, named, bytes.Equal), "MoQ and WebSocket select the same signed assets")
			require.Empty(t, renditions)
		})
	}
}
