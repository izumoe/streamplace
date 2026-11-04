package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/go-gst/go-glib/pkg/gobject/v2"
	"github.com/go-gst/go-gst/pkg/gst"
	app "github.com/go-gst/go-gst/pkg/gstapp"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/gstpipeline"
)

// These access units come from a real temporal-direct encoder. Caps retain its
// codec_data for AVC, while byte-stream samples carry their own parameter sets.
func h264MetadataSamples(t *testing.T, format string) (*gst.Caps, [][]byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	element, err := gst.ParseLaunch(fmt.Sprintf("videotestsrc num-buffers=9 pattern=ball ! video/x-raw,width=160,height=120,framerate=30/1 ! x264enc bframes=2 b-adapt=false b-pyramid=false key-int-max=30 speed-preset=fast option-string=direct=temporal:scenecut=0:threads=1 ! h264parse config-interval=-1 ! video/x-h264,stream-format=%s,alignment=au ! appsink name=sink sync=false enable-last-sample=false", format))
	require.NoError(t, err)
	pipeline := element.(gst.Pipeline)
	defer pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone)
	var caps *gst.Caps
	var samples [][]byte
	pipeline.GetByName("sink").(app.AppSink).SetCallbacks(&app.AppSinkCallbacks{NewSample: func(sink app.AppSink) gst.FlowReturn {
		sample := sink.PullSample()
		if sample == nil {
			return gst.FlowEOS
		}
		data, err := copyBufferData(sample.GetBuffer())
		if err != nil {
			return gst.FlowError
		}
		defer gst.UnsafeSampleUnref(sample)
		if caps == nil {
			caps = sample.GetCaps().Copy()
		}
		samples = append(samples, data)
		return gst.FlowOK
	}})
	require.NotEqual(t, gst.StateChangeFailure, pipeline.SetState(gst.StatePlaying))
	require.NoError(t, gstpipeline.HandleBusMessages(ctx, pipeline))
	require.Equal(t, gst.StateChangeSuccess, pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone))
	require.NotNil(t, caps)
	t.Cleanup(func() { gst.UnsafeCapsUnref(caps) })
	require.Len(t, samples, 9)
	return caps, samples
}

func h264MetadataJSON(t *testing.T, caps *gst.Caps, samples [][]byte, compact bool) ([][]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	element, err := gst.ParseLaunch("appsrc name=src ! h2642json name=json ! appsink name=sink sync=false enable-last-sample=false")
	if err != nil {
		return nil, err
	}
	pipeline := element.(gst.Pipeline)
	defer pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone)
	converter := pipeline.GetByName("json")
	if compact {
		if converter.ObjectProperty("b-frames-only") == gobject.InvalidValue {
			return nil, fmt.Errorf("h2642json b-frames-only property is unavailable")
		}
		converter.SetObjectProperty("b-frames-only", true)
	}
	var outputs [][]byte
	pipeline.GetByName("sink").(app.AppSink).SetCallbacks(&app.AppSinkCallbacks{NewSample: func(sink app.AppSink) gst.FlowReturn {
		sample := sink.PullSample()
		if sample == nil {
			return gst.FlowEOS
		}
		data, err := copyBufferData(sample.GetBuffer())
		if err != nil {
			return gst.FlowError
		}
		defer gst.UnsafeSampleUnref(sample)
		outputs = append(outputs, data)
		return gst.FlowOK
	}})
	src := pipeline.GetByName("src").(app.AppSrc)
	src.AppSrcSetCaps(caps)
	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		return nil, fmt.Errorf("start h2642json pipeline")
	}
	for i, data := range samples {
		buffer, err := gstpipeline.NewBufferFromBytes(data)
		if err != nil {
			return nil, err
		}
		buffer.SetPTS(gst.ClockTime(i) * gst.ClockTime(time.Second/30))
		buffer.SetDuration(gst.ClockTime(time.Second / 30))
		if flow := src.PushBuffer(buffer); flow != gst.FlowOK {
			return nil, fmt.Errorf("push h2642json input: %s", flow)
		}
	}
	if flow := src.EndOfStream(); flow != gst.FlowOK {
		return nil, fmt.Errorf("end h2642json input: %s", flow)
	}
	err = gstpipeline.HandleBusMessages(ctx, pipeline)
	if pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone) == gst.StateChangeFailure {
		return nil, fmt.Errorf("stop h2642json pipeline")
	}
	return outputs, err
}

func TestH264MetadataFullJSON(t *testing.T) {
	for _, format := range []string{"byte-stream", "avc", "avc3"} {
		t.Run(format, func(t *testing.T) {
			caps, samples := h264MetadataSamples(t, format)
			outputs, err := h264MetadataJSON(t, caps, samples, false)
			require.NoError(t, err)
			require.Len(t, outputs, len(samples))
			temporalB := false
			for _, output := range outputs {
				var object map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(output, &object))
				require.Contains(t, object, "sps")
				require.Contains(t, object, "pps")
				require.Contains(t, object, "slice header")
				var slice struct {
					Type    int  `json:"type"`
					Spatial bool `json:"direct spatial mv pred flag"`
				}
				require.NoError(t, json.Unmarshal(object["slice header"], &slice))
				if slice.Type%5 == 1 {
					require.False(t, slice.Spatial, "every fixture B slice must use temporal direct prediction")
				}
				temporalB = temporalB || slice.Type%5 == 1 && !slice.Spatial
			}
			require.True(t, temporalB, "fixture must exercise B slices with temporal direct prediction")
		})
	}
}

func TestH264MetadataCompactJSON(t *testing.T) {
	for _, format := range []string{"byte-stream", "avc", "avc3"} {
		t.Run(format, func(t *testing.T) {
			caps, samples := h264MetadataSamples(t, format)
			full, err := h264MetadataJSON(t, caps, samples, false)
			require.NoError(t, err)
			require.Len(t, full, len(samples))
			var want []bool
			var bFrame, pFrame []byte
			for i, output := range full {
				var object struct {
					Slice struct {
						Type int `json:"type"`
					} `json:"slice header"`
				}
				require.NoError(t, json.Unmarshal(output, &object))
				isB := object.Slice.Type%5 == 1
				want = append(want, isB)
				if isB {
					bFrame = samples[i]
				} else if object.Slice.Type%5 == 0 {
					pFrame = samples[i]
				}
			}
			require.NotEmpty(t, bFrame)
			require.NotEmpty(t, pFrame)
			// A later non-B NAL must not erase B detection; the next buffer must reset it.
			input := append([][]byte(nil), samples...)
			input = append(input, append(bytes.Clone(bFrame), pFrame...), pFrame)
			want = append(want, true, false)
			outputs, err := h264MetadataJSON(t, caps, input, true)
			require.NoError(t, err)
			require.Len(t, outputs, len(input))
			for i, output := range outputs {
				require.LessOrEqual(t, len(output), 32, "compact output stays bounded independent of slice syntax")
				var object map[string]bool
				require.NoError(t, json.Unmarshal(output, &object))
				require.Equal(t, map[string]bool{"has-b-frames": want[i]}, object)
			}
		})
	}
}

func TestMediaDataParserTemporalDirectBFrames(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	input := runSynthPipeline(t, ctx, "videotestsrc num-buffers=9 pattern=ball ! video/x-raw,width=160,height=120,framerate=30/1 ! x264enc bframes=2 b-adapt=false b-pyramid=false key-int-max=30 speed-preset=fast option-string=direct=temporal:scenecut=0:threads=1 ! h264parse ! mp4mux name=mux fragment-duration=100 ! appsink name=sink sync=false audiotestsrc num-buffers=16 samplesperbuffer=1024 ! audio/x-raw,rate=48000,channels=1 ! fdkaacenc ! aacparse ! mux.")
	metadata, err := ParseSegmentMediaData(ctx, input)
	require.NoError(t, err)
	require.Len(t, metadata.Video, 1)
	require.True(t, metadata.Video[0].BFrames, "temporal-direct B slices have a false spatial prediction flag")
}

func TestH264MetadataRejectsMalformedNALs(t *testing.T) {
	previousVerbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "1"))
	t.Cleanup(func() { require.NoError(t, flag.Set("v", previousVerbosity)) })
	for _, format := range []string{"byte-stream", "avc", "avc3"} {
		t.Run(format, func(t *testing.T) {
			caps, samples := h264MetadataSamples(t, format)
			full, err := h264MetadataJSON(t, caps, samples, false)
			require.NoError(t, err)
			var bFrame []byte
			for i, output := range full {
				var object struct {
					Slice struct {
						Type int `json:"type"`
					} `json:"slice header"`
				}
				require.NoError(t, json.Unmarshal(output, &object))
				if object.Slice.Type%5 == 1 {
					bFrame = samples[i]
					break
				}
			}
			require.NotEmpty(t, bFrame)
			cases := []struct {
				name string
				nal  []byte
			}{{"SPS", []byte{0x67}}, {"PPS", []byte{0x68}}, {"slice", []byte{0x41}}, {"slice-header-tail", []byte{0x41, 0xe0}}, {"truncated", nil}}
			if format != "byte-stream" {
				cases = append(cases, struct {
					name string
					nal  []byte
				}{"zero-length", nil})
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					var logs logCapture
					previous := slog.Default()
					slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
					defer slog.SetDefault(previous)
					var bad []byte
					if format == "byte-stream" {
						bad = append([]byte{0, 0, 0, 1}, test.nal...)
					} else {
						bad = make([]byte, 4)
						size := len(test.nal)
						if test.name == "truncated" {
							size = 100
						}
						binary.BigEndian.PutUint32(bad, uint32(size))
						bad = append(bad, test.nal...)
					}
					input := [][]byte{samples[0], append(bytes.Clone(bFrame), bad...)}
					outputs, err := h264MetadataJSON(t, caps, input, true)
					require.ErrorContains(t, err, "gstreamer error")
					require.Len(t, outputs, 1, "a valid B NAL must not cause early acceptance of its malformed tail")
					require.Contains(t, logs.String(), "gstreamer error")
				})
			}
		})
	}
}

func TestH264MetadataCompactAVCTwoByteLengths(t *testing.T) {
	caps, samples := h264MetadataSamples(t, "avc")
	full, err := h264MetadataJSON(t, caps, samples, false)
	require.NoError(t, err)
	configBuffer, ok := caps.GetStructure(0).GetValue("codec_data").(*gst.Buffer)
	require.True(t, ok)
	config, err := copyBufferData(configBuffer)
	require.NoError(t, err)
	gst.UnsafeBufferUnref(configBuffer)
	require.GreaterOrEqual(t, len(config), 7)
	require.Equal(t, byte(3), config[4]&3, "encoder must supply four-byte source lengths")
	config[4] = config[4]&0xfc | 1
	twoCaps := caps.Copy()
	defer gst.UnsafeCapsUnref(twoCaps)
	replacement, err := gstpipeline.NewBufferFromBytes(config)
	require.NoError(t, err)
	twoCaps.GetStructure(0).SetValue("codec_data", replacement)
	gst.UnsafeBufferUnref(replacement)
	var input [][]byte
	for _, sample := range samples {
		var output []byte
		for offset := 0; offset < len(sample); {
			require.GreaterOrEqual(t, len(sample)-offset, 4)
			size := int(binary.BigEndian.Uint32(sample[offset:]))
			require.Positive(t, size)
			require.LessOrEqual(t, size, 65535)
			require.LessOrEqual(t, offset+4+size, len(sample))
			output = append(output, byte(size>>8), byte(size))
			output = append(output, sample[offset+4:offset+4+size]...)
			offset += 4 + size
		}
		input = append(input, output)
	}
	outputs, err := h264MetadataJSON(t, twoCaps, input, true)
	require.NoError(t, err)
	require.Len(t, outputs, len(full))
	for i, output := range outputs {
		var original struct {
			Slice struct {
				Type int `json:"type"`
			} `json:"slice header"`
		}
		require.NoError(t, json.Unmarshal(full[i], &original))
		var compact map[string]bool
		require.NoError(t, json.Unmarshal(output, &compact))
		require.Equal(t, map[string]bool{"has-b-frames": original.Slice.Type%5 == 1}, compact)
	}
}

func TestH264MetadataCompactAnnexBPadding(t *testing.T) {
	caps, samples := h264MetadataSamples(t, "byte-stream")
	original, err := h264MetadataJSON(t, caps, samples, true)
	require.NoError(t, err)
	var padded [][]byte
	for _, sample := range samples {
		data := append([]byte{0, 0}, sample...)
		padded = append(padded, append(data, 0, 0, 0))
	}
	outputs, err := h264MetadataJSON(t, caps, padded, true)
	require.NoError(t, err)
	require.Equal(t, original, outputs, "Annex B leading and trailing zero bytes preserve the compact result")
}

func TestH264MetadataRejectsMalformedCodecData(t *testing.T) {
	previousVerbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "1"))
	t.Cleanup(func() { require.NoError(t, flag.Set("v", previousVerbosity)) })
	for _, format := range []string{"avc", "avc3"} {
		t.Run(format, func(t *testing.T) {
			caps, samples := h264MetadataSamples(t, format)
			for _, bad := range [][]byte{{1, 100}, {1, 100, 0, 31, 0xff, 0xe1, 0, 1, 0x67, 1, 0, 1, 0x68}} {
				t.Run(fmt.Sprintf("%x", bad), func(t *testing.T) {
					var logs logCapture
					previous := slog.Default()
					slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
					defer slog.SetDefault(previous)
					invalid := caps.Copy()
					defer gst.UnsafeCapsUnref(invalid)
					config, err := gstpipeline.NewBufferFromBytes(bad)
					require.NoError(t, err)
					invalid.GetStructure(0).SetValue("codec_data", config)
					gst.UnsafeBufferUnref(config)
					outputs, err := h264MetadataJSON(t, invalid, samples, true)
					require.ErrorContains(t, err, "gstreamer error")
					require.Empty(t, outputs, "malformed negotiated configuration must not fall back to in-band parameter sets")
					require.Contains(t, logs.String(), "gstreamer error")
				})
			}
		})
	}
}
