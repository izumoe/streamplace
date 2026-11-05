package media

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// makeOpenGOPFMP4 synthesizes what a broadcast encoder sends at scene cuts:
// open-GOP I-frames, I-slices that are not IDRs (x264 open-gop makes every
// keyframe after the first one of those), between IDRs, alongside AAC
// audio, in a fragmented MP4. h264parse flags them as keyframes.
func makeOpenGOPFMP4(t *testing.T, ctx context.Context) []byte {
	t.Helper()
	desc := "videotestsrc num-buffers=90 pattern=ball ! video/x-raw,width=320,height=240,framerate=30/1 ! " +
		"x264enc key-int-max=10 bframes=2 b-adapt=false speed-preset=ultrafast option-string=open-gop=1:scenecut=0 ! h264parse ! mp4mux name=mux fragment-duration=500 ! appsink name=sink " +
		"audiotestsrc num-buffers=130 samplesperbuffer=1024 ! audio/x-raw,rate=48000,channels=2 ! fdkaacenc ! aacparse ! mux."
	return runSynthPipeline(t, ctx, desc)
}

// videoFirstSampleNALs returns the NAL types of the first video sample of
// each signed segment's first event.
func videoFirstSampleNALs(t *testing.T, ctx context.Context, seg []byte) []int {
	t.Helper()
	evs, err := unwrapMuxlEvents(ctx, seg)
	require.NoError(t, err)
	for _, ev := range evs {
		if ev.Type != "segment" && ev.Type != "signed-segment" {
			continue
		}
		vb := ev.Tracks["1"]
		if len(vb) == 0 {
			return nil
		}
		return firstSampleNALTypes(vb)
	}
	return nil
}

// firstSampleNALTypes walks a canonical track fragment to its first sample
// and lists that sample's NAL types (length-prefixed).
func firstSampleNALTypes(b []byte) []int {
	var size int
	var mdat []byte
	walkBoxes(b, func(typ string, body []byte) bool {
		switch typ {
		case "moof":
			walkBoxes(body, func(t2 string, traf []byte) bool {
				if t2 != "traf" {
					return true
				}
				walkBoxes(traf, func(t3 string, trun []byte) bool {
					if t3 != "trun" || size != 0 || len(trun) < 8 {
						return true
					}
					flags := uint32(trun[1])<<16 | uint32(trun[2])<<8 | uint32(trun[3])
					p := 8
					if flags&1 != 0 {
						p += 4
					}
					if flags&4 != 0 {
						p += 4
					}
					if flags&0x100 != 0 {
						p += 4
					}
					if flags&0x200 != 0 && p+4 <= len(trun) {
						size = int(uint32(trun[p])<<24 | uint32(trun[p+1])<<16 | uint32(trun[p+2])<<8 | uint32(trun[p+3]))
					}
					return true
				})
				return true
			})
		case "mdat":
			if mdat == nil {
				mdat = body
			}
		}
		return true
	})
	if size == 0 || len(mdat) < size {
		return nil
	}
	return h264NALTypes(mdat[:size])
}

// Every signed segment starts on an IDR, even when the encoder's keyframes
// include open-GOP I-frames: those stay inside the GoP of the IDR before
// them rather than starting a segment no decoder could begin on.
func TestIngestOpenGOPSegmentsStartOnIDR(t *testing.T) {
	ctx := context.Background()
	mp4 := makeOpenGOPFMP4(t, ctx)
	segs, err := runMP4ThroughIngestWorkerSegments(t, mp4, false)
	require.NoError(t, err)
	require.NotEmpty(t, segs)
	var nonIDR int
	for i, seg := range segs {
		nals := videoFirstSampleNALs(t, ctx, seg)
		require.NotEmpty(t, nals, "segment %d has a first video sample", i)
		hasIDR := false
		for _, n := range nals {
			if n == 5 {
				hasIDR = true
			}
		}
		if !hasIDR {
			nonIDR++
			t.Logf("segment %d starts with NALs %v", i, nals)
		}
	}
	require.Zero(t, nonIDR, "segments starting without an IDR")
	require.LessOrEqual(t, len(segs), 2, "one IDR in 90 frames (the other 8 keyframes are open-GOP I-frames): one GoP, not a segment per I-frame")
}

// Completion can publish before the encoder drains. Its signed continuation
// must preserve the same uninterrupted audio, including through open-GOP
// I-frames that stay inside the source's IDR-bounded segment.
func TestIngestOpenGOPCompletionStaysAligned(t *testing.T) {
	ctx := context.Background()
	mp4 := makeOpenGOPFMP4(t, ctx)
	segs, err := runMP4ThroughIngestWorkerSegments(t, mp4, true)
	require.NoError(t, err)
	require.NotEmpty(t, segs)
	for i, seg := range segs {
		_, err := ValidateMP4MediaC2PA(ctx, seg)
		require.NoError(t, err, "source, completion, and continuation signatures must validate")
		want := uninterruptedAudioPackets(t, ctx, [][]byte{seg}, "aac")
		aac := canonicalAudioPackets(t, ctx, seg, "aac")
		opus := canonicalAudioPackets(t, ctx, seg, "opus")
		require.NotEmpty(t, opus)
		require.NotEmpty(t, want)
		require.Len(t, aac, len(want), "segment %d cannot lose or repeat an encoded packet", i)
		for n, packet := range aac {
			require.Equal(t, want[n].data, packet.data, "segment %d packet %d payload", i, n)
			require.InDelta(t, want[n].pts, packet.pts, float64(time.Second)/48000, "packet %d PTS", n)
			duration := want[n].duration
			if n+1 < len(want) {
				// The encoder's initial duration can overlap its next PTS while
				// clipping priming; the signed clock follows actual packet starts.
				require.Greater(t, want[n+1].pts, want[n].pts)
				duration = want[n+1].pts - want[n].pts
			}
			require.InDelta(t, duration, packet.duration, float64(time.Second)/48000, "packet %d duration", n)
			if n > 0 {
				require.Equal(t, aac[n-1].pts+aac[n-1].duration, packet.pts, "packet %d has no gap or overlap", n)
			}
		}
		const tolerance = float64(100 * time.Millisecond)
		require.InDelta(t, opus[0].pts, aac[0].pts, tolerance, "segment %d audio starts together", i)
		sourceEnd := opus[len(opus)-1].pts + opus[len(opus)-1].duration
		completedEnd := aac[len(aac)-1].pts + aac[len(aac)-1].duration
		require.InDelta(t, sourceEnd, completedEnd, tolerance, "segment %d audio ends together", i)
		t.Logf("segment %d: %d AAC packets match uninterrupted encoding, Opus %.3fs AAC %.3fs", i, len(aac), float64(sourceEnd-opus[0].pts)/float64(time.Second), float64(completedEnd-aac[0].pts)/float64(time.Second))
	}
	require.LessOrEqual(t, len(segs), 2, "as many completed segments as source segments: one GoP")
}
