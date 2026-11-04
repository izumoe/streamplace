package media

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	app "github.com/go-gst/go-gst/pkg/gstapp"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/gstpipeline"
	"stream.place/streamplace/pkg/muxl"
)

// Boundary events may regroup encoded packets, but must not change the codec's
// continuous output or its media clock. The reference has no boundary events,
// signatures, or per-source callbacks: it feeds the same bytes once, then EOS.
func TestStreamTranscoderPreservesContinuousAudio(t *testing.T) {
	for _, test := range []struct {
		fixture string
		target  string
	}{
		{"ertmp-red-h264.mp4", "opus"},
		{"h264-opus-frag.mp4", "aac"},
	} {
		for _, origin := range []time.Duration{0, 300 * time.Second} {
			t.Run(fmt.Sprintf("%s/origin=%s", test.target, origin), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				source := signedAudioSourceAt(t, ctx, test.fixture, origin)
				require.GreaterOrEqual(t, len(source), 2)
				want := uninterruptedAudioPackets(t, ctx, source, test.target)
				require.NotEmpty(t, want)
				cert, key := nodeSignerForTest(t)
				mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "node.test"}}
				type completion struct {
					token int
					data  []byte
					tail  bool
				}
				var completed []completion
				tr := mm.newStreamTranscoder(ctx, test.target, cert, key, func(token any, data []byte, tail bool) {
					completed = append(completed, completion{token.(int), data, tail})
				})
				t.Cleanup(func() { require.NoError(t, tr.Close()) })
				for i, segment := range source {
					require.NoError(t, tr.Feed(segment, i))
				}
				require.NoError(t, tr.Close())
				require.Len(t, completed, len(source)+1, "EOS must deliver the retained codec tail separately")
				require.True(t, completed[len(completed)-1].tail)
				var got []audioPacket
				for i, output := range completed {
					if !output.tail {
						require.Equal(t, i, output.token)
						require.True(t, bytes.HasPrefix(output.data, source[i]), "source signatures and media remain byte-for-byte unchanged")
					} else {
						require.Equal(t, len(source)-1, output.token)
					}
					verification, err := muxl.RunMuxlVerify(ctx, bytes.NewReader(output.data))
					require.NoError(t, err)
					_, err = parseVerifiedSegments([]byte(verification))
					require.NoError(t, err, "every independently delivered asset must validate, including the audio-only tail")
					got = append(got, canonicalAudioPackets(t, ctx, output.data, test.target)...)
				}
				require.Len(t, got, len(want), "no encoded packet can be lost or repeated at a boundary or EOS")
				for i := range want {
					require.Equal(t, want[i].data, got[i].data, "packet %d payload", i)
					// Canonical MP4 clocks quantize nanoseconds to codec ticks.
					require.InDelta(t, want[i].pts, got[i].pts, float64(time.Second)/48000, "packet %d PTS", i)
					require.InDelta(t, want[i].duration, got[i].duration, float64(time.Second)/48000, "packet %d duration", i)
					if i > 0 {
						require.Equal(t, got[i-1].pts+got[i-1].duration, got[i].pts, "packet %d must follow its predecessor without a gap or overlap", i)
					}
				}
				require.GreaterOrEqual(t, got[0].pts, uint64(origin), "transcoding must preserve a nonzero source timeline")
			})
		}
	}
}

func signedAudioSourceAt(t *testing.T, ctx context.Context, fixture string, origin time.Duration) [][]byte {
	t.Helper()
	input, err := os.ReadFile(getFixture(fixture))
	require.NoError(t, err)
	canonical, err := muxl.RunMuxlCanonicalize(ctx, input, nil)
	require.NoError(t, err)
	events, err := unwrapMuxlEvents(ctx, canonical)
	require.NoError(t, err)
	cat := catalogFromEvents(events)
	scales := map[string]uint32{}
	for _, video := range cat.Video.Renditions {
		scales[strconv.FormatUint(uint64(video.TrackID()), 10)] = video.Timescale()
	}
	for _, audio := range cat.Audio.Renditions {
		scales[strconv.FormatUint(uint64(audio.TrackID()), 10)] = audio.Timescale()
	}
	var shifted []byte
	for _, event := range events {
		for id, track := range event.Tracks {
			shiftTfdt(track, int64(origin/time.Second)*int64(scales[id]))
		}
		shifted = append(shifted, concatTracksSorted(event.Tracks)...)
	}
	var presentation bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(shifted), "fmp4", &presentation))
	signer := newBareSegmentSigner(t)
	output := make(chan *muxl.MuxlEvent, 16)
	done := make(chan error, 1)
	go func() {
		done <- signer.SignSegmentStream(ctx, bytes.NewReader(presentation.Bytes()), output)
		close(output)
	}()
	var source [][]byte
	for event := range output {
		if event.Type == "signed-segment" {
			source = append(source, concatTracksSorted(event.Tracks))
		}
	}
	require.NoError(t, <-done)
	require.NotEmpty(t, source)
	first, err := unwrapMuxlEvents(ctx, source[0])
	require.NoError(t, err)
	video, ok := primaryVideoTrack(catalogFromEvents(first), 0, "")
	require.True(t, ok)
	id := strconv.FormatUint(uint64(video.TrackID()), 10)
	for _, event := range first {
		if len(event.Tracks[id]) > 0 {
			require.GreaterOrEqual(t, event.FirstDecodeTimes[id], uint64(origin/time.Second)*uint64(video.Timescale()), "fixture must retain the requested source origin")
			break
		}
	}
	return source
}

func uninterruptedAudioPackets(t *testing.T, ctx context.Context, source [][]byte, target string) []audioPacket {
	t.Helper()
	pipeline, err := buildAudioTranscodePipeline(target)
	require.NoError(t, err)
	return uninterruptedAudioPacketsWithPipeline(t, ctx, source, target, pipeline)
}

func uninterruptedAudioPacketsWithPipeline(t *testing.T, ctx context.Context, source [][]byte, target string, pipeline gst.Pipeline) []audioPacket {
	t.Helper()
	var input bytes.Buffer
	for i, segment := range source {
		events, err := unwrapMuxlEvents(ctx, segment)
		require.NoError(t, err)
		selected, err := selectEventTracks(events, 0, "", target == "aac")
		require.NoError(t, err)
		if i == 0 {
			require.NoError(t, muxl.RunMuxlWrapInit(ctx, bytes.NewReader(selected), &input))
		}
		input.Write(selected)
	}
	defer pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone)
	pipeline.GetByName("src").(app.AppSrc).SetCallbacks(&app.AppSrcCallbacks{
		NeedData: gstpipeline.ReaderNeedDataIncremental(ctx, bytes.NewReader(input.Bytes())),
	})
	var packets []audioPacket
	var readErr error
	pipeline.GetByName("sink").(app.AppSink).SetCallbacks(&app.AppSinkCallbacks{NewSample: func(sink app.AppSink) gst.FlowReturn {
		sample := sink.PullSample()
		if sample == nil {
			return gst.FlowEOS
		}
		defer gst.UnsafeSampleUnref(sample)
		buffer := sample.GetBuffer()
		data, err := copyBufferData(buffer)
		if err != nil {
			readErr = err
			return gst.FlowError
		}
		packets = append(packets, audioPacket{data: data, pts: uint64(buffer.PTS()), duration: uint64(buffer.Duration())})
		return gst.FlowOK
	}})
	if header := pipeline.GetByName("header"); header != nil {
		header.(app.AppSink).SetCallbacks(&app.AppSinkCallbacks{NewSample: gstpipeline.WriterNewSample(ctx, io.Discard)})
	}
	require.NotEqual(t, gst.StateChangeFailure, pipeline.SetState(gst.StatePlaying))
	require.NoError(t, gstpipeline.HandleBusMessages(ctx, pipeline))
	require.NoError(t, readErr)
	return packets
}

func canonicalAudioPackets(t *testing.T, ctx context.Context, data []byte, codec string) []audioPacket {
	t.Helper()
	events, err := unwrapMuxlEvents(ctx, data)
	require.NoError(t, err)
	var id string
	var scale uint64
	for _, audio := range catalogFromEvents(events).Audio.Renditions {
		if codec == "opus" && isOpusCodec(audio.Codec) || codec == "aac" && isAACCodec(audio.Codec) {
			id = strconv.FormatUint(uint64(audio.TrackID()), 10)
			scale = uint64(audio.Timescale())
		}
	}
	require.NotEmpty(t, id)
	require.NotZero(t, scale)
	var packets []audioPacket
	for _, event := range events {
		track, ok := event.Tracks[id]
		if !ok {
			continue
		}
		samples := event.Samples[id]
		require.Len(t, samples.Sizes, len(samples.Durations))
		require.Len(t, samples.Offsets, len(samples.Durations))
		ticks := event.FirstDecodeTimes[id]
		for i, duration := range samples.Durations {
			start := samples.Offsets[i]
			end := start + uint64(samples.Sizes[i])
			require.LessOrEqual(t, end, uint64(len(track)))
			packets = append(packets, audioPacket{
				data:     bytes.Clone(track[start:end]),
				pts:      ticks * uint64(time.Second) / scale,
				duration: (ticks+uint64(duration))*uint64(time.Second)/scale - ticks*uint64(time.Second)/scale,
			})
			ticks += uint64(duration)
		}
	}
	return packets
}
