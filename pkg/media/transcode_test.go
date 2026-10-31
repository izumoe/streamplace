package media

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/crypto/signers"
	"stream.place/streamplace/pkg/gstinit"
	"stream.place/streamplace/pkg/localdb"
)

func TestAudioCompletionTargetFromMedia(t *testing.T) {
	for _, test := range []struct {
		name   string
		video  bool
		codecs []string
		target string
	}{
		{"AAC", true, []string{"aac"}, "opus"},
		{"Opus", true, []string{"opus"}, "aac"},
		{"both", true, []string{"aac", "opus"}, ""},
		{"reordered", true, []string{"opus", "aac"}, ""},
		{"duplicate AAC", true, []string{"aac", "aac"}, "opus"},
		{"other codec", true, []string{"flac"}, ""},
		{"no audio", true, nil, ""},
		{"audio only", false, []string{"aac"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := &localdb.SegmentMediaData{}
			if test.video {
				data.Video = []*localdb.SegmentMediadataVideo{{Codec: "av1"}}
			}
			for _, codec := range test.codecs {
				data.Audio = append(data.Audio, &localdb.SegmentMediadataAudio{Codec: codec})
			}
			target, need := audioCompletionTargetFromMedia(data)
			require.Equal(t, test.target, target)
			require.Equal(t, test.target != "", need)
		})
	}
}

func TestValidatedAudioCompletionTarget(t *testing.T) {
	mm := &MediaManager{}
	for _, fixture := range []string{"h264-opus-frag.mp4", "av1-aac-frag.mp4"} {
		t.Run(fixture, func(t *testing.T) {
			segments := allSignedBareSegments(t, t.Context(), newBareSegmentSigner(t), getFixture(fixture))
			for _, segment := range segments {
				valid, err := ValidateMP4Media(t.Context(), segment)
				require.NoError(t, err)
				want, need := mm.audioCompletionTarget(t.Context(), segment)
				got, gotNeed := audioCompletionTargetFromMedia(valid.MediaData)
				require.Equal(t, want, got)
				require.Equal(t, need, gotNeed)
			}
		})
	}
}

func TestAudioTranscodePipelineReleasesQueues(t *testing.T) {
	withNoGSTLeaks(t, func() {
		for _, target := range []string{"aac", "opus"} {
			for _, codec := range []string{"avc1", "av01", ""} {
				pipeline, err := buildAudioTranscodePipeline(target, codec)
				require.NoError(t, err)
				require.Equal(t, gst.StateChangeSuccess, pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone))
			}
		}
	})
}

func TestStreamTranscoderAACSource(t *testing.T) {
	withNoGSTLeaks(t, func() {
		ctx := context.Background()
		input := makeH264AACFMP4(t, ctx, getFixture("5sec.mp4"))
		inputPath := filepath.Join(t.TempDir(), "aac.mp4")
		require.NoError(t, os.WriteFile(inputPath, input, 0o600))
		ms := newBareSegmentSigner(t)
		segs := allSignedBareSegments(t, ctx, ms, inputPath)
		require.NotEmpty(t, segs)
		keyPEM, err := signers.MarshalES256KPrivateKeyPEM(ms.Signer)
		require.NoError(t, err)
		mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "test.example.com"}}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var completed [][]byte
		tr := mm.newStreamTranscoder(ctx, "opus", ms.Cert, keyPEM, func(_ any, seg []byte) {
			completed = append(completed, seg)
		})
		for i, seg := range segs {
			require.NoError(t, tr.Feed(seg, i))
		}
		require.NoError(t, tr.Close())
		require.NoError(t, ctx.Err(), "both demux branches finish before the watchdog")
		require.NotEmpty(t, completed)
		for _, seg := range completed {
			codecs := audioCodecsOf(t, context.Background(), seg)
			require.Len(t, codecs, 2)
			require.True(t, slices.ContainsFunc(codecs, isAACCodec))
			require.True(t, slices.ContainsFunc(codecs, isOpusCodec))
		}
	})
}

func BenchmarkAudioTranscodePipeline(b *testing.B) {
	gstinit.InitGST()
	for _, target := range []string{"aac", "opus"} {
		b.Run(target, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				pipeline, err := buildAudioTranscodePipeline(target, "avc1")
				if err != nil {
					b.Fatal(err)
				}
				if state := pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone); state != gst.StateChangeSuccess {
					b.Fatalf("failed to stop pipeline: %v", state)
				}
			}
		})
	}
}

// audioCodecsOf returns the sorted distinct audio codecs in a bare .m4s segment.
func audioCodecsOf(t *testing.T, ctx context.Context, seg []byte) []string {
	t.Helper()
	events, err := unwrapMuxlEvents(ctx, seg)
	require.NoError(t, err)
	cat, _ := catalogAndTracks(events)
	require.NotNil(t, cat, "segment has a catalog")
	var out []string
	if cat.Audio != nil {
		for _, a := range cat.Audio.Renditions {
			out = append(out, a.Codec)
		}
	}
	sort.Strings(out)
	return out
}

func TestStreamTranscoderCloseWithoutInput(t *testing.T) {
	mm := &MediaManager{cli: &config.CLI{}}
	tr := mm.newStreamTranscoder(t.Context(), "aac", nil, nil, func(any, []byte) {
		t.Error("an empty stream must not produce a completion")
	})
	require.NoError(t, tr.Close())
	require.NoError(t, tr.Close())
	require.ErrorContains(t, tr.Feed(nil, nil), "closed")
}
