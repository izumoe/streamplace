package media

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	app "github.com/go-gst/go-gst/pkg/gstapp"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/crypto/signers"
	"stream.place/streamplace/pkg/gstinit"
	"stream.place/streamplace/pkg/gstpipeline"
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

func TestAudioTranscodeTimestampProbeReleasesBuffersBeforeGC(t *testing.T) {
	previousGC := debug.SetGCPercent(-1)
	t.Cleanup(func() { debug.SetGCPercent(previousGC) })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	input, err := os.ReadFile(getFixture("av1-aac-frag.mp4"))
	require.NoError(t, err)
	// AV1 isolates the timestamp probe from the separate H264 IDR probe.
	pipeline, err := buildAudioTranscodePipeline("opus", "av01")
	require.NoError(t, err)
	defer pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone)
	vout := pipeline.GetByName("vout")
	index := 0
	vout.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buffer := info.GetBuffer()
		if buffer == nil {
			return gst.PadProbeOK
		}
		defer gst.UnsafeBufferUnref(buffer)
		switch index {
		case 0:
			buffer.SetPTS(0)
		case 1:
			buffer.SetPTS(gst.ClockTimeNone)
			buffer.SetDTS(0)
			buffer.SetDuration(gst.ClockTimeNone)
		case 2:
			buffer.SetPTS(gst.ClockTimeNone)
			buffer.SetDTS(gst.ClockTimeNone)
			buffer.SetDuration(0)
		case 3:
			buffer.SetPTS(0)
		}
		index++
		return gst.PadProbeOK
	})
	heldCh := make(chan *gst.Buffer, 4)
	vout.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buffer := info.GetBuffer()
		if len(heldCh) < cap(heldCh) {
			heldCh <- buffer
		} else if buffer != nil {
			gst.UnsafeBufferUnref(buffer)
		}
		return gst.PadProbeOK
	})
	pipeline.GetByName("src").(app.AppSrc).SetCallbacks(&app.AppSrcCallbacks{NeedData: gstpipeline.ReaderNeedDataIncremental(ctx, bytes.NewReader(input))})
	pipeline.GetByName("sink").(app.AppSink).SetCallbacks(&app.AppSinkCallbacks{NewSample: gstpipeline.WriterNewSample(ctx, io.Discard)})
	require.NotEqual(t, gst.StateChangeFailure, pipeline.SetState(gst.StatePlaying))
	require.NoError(t, gstpipeline.HandleBusMessages(ctx, pipeline))
	require.Equal(t, gst.StateChangeSuccess, pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone))
	defer func() {
		for len(heldCh) > 0 {
			if buffer := <-heldCh; buffer != nil {
				gst.UnsafeBufferUnref(buffer)
			}
		}
	}()
	require.Len(t, heldCh, 4)
	for i := range 4 {
		buffer := <-heldCh
		require.NotNil(t, buffer)
		defer gst.UnsafeBufferUnref(buffer)
		require.Equal(t, gst.ClockTime(i)*gst.ClockTime(time.Millisecond), buffer.PTS(), "missing and regressive timestamps must advance without changing the first timestamp")
		if i == 1 || i == 2 {
			require.Equal(t, gst.ClockTime(time.Millisecond), buffer.Duration())
		}
		require.True(t, gst.UnsafeMiniObjectFromGlibBorrow(gst.UnsafeBufferToGlibNone(buffer)).IsWritable(), "the timestamp probe must release its reference before Go GC, frame %d", i)
	}
}
