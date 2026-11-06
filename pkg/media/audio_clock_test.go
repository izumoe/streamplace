package media

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4/seekablebuffer"
	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/muxl"
)

// Container timestamp rounding must not turn continuous encoder samples into
// overlapping signed batches. A real source discontinuity must still survive.
func TestStreamTranscoderRoundedAudioClock(t *testing.T) {
	for _, test := range []struct{ fixture, target, decode, encode string }{
		{"ertmp-red-h264.mp4", "opus", "aacparse ! fdkaacdec", "opusenc"},
		{"h264-opus-frag.mp4", "aac", "opusparse ! opusdec", "fdkaacenc"},
	} {
		for _, clock := range []struct {
			gap      time.Duration
			internal bool
			drift    bool
		}{{0, false, false}, {100 * time.Millisecond, false, false}, {100 * time.Millisecond, true, false}, {0, false, true}} {
			gap := clock.gap
			t.Run(fmt.Sprintf("%s/gap=%s/internal=%t/drift=%t", test.target, gap, clock.internal, clock.drift), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				source := roundedAudioSource(t, ctx, test.fixture, gap, clock.internal, clock.drift)
				native, err := gst.ParseLaunch(fmt.Sprintf("appsrc name=src ! qtdemux name=demux demux.video_0 ! queue ! fakesink sync=false async=false demux.audio_0 ! queue ! %s ! audioconvert ! audioresample ! %s perfect-timestamp=false ! appsink name=sink sync=false async=false enable-last-sample=false", test.decode, test.encode))
				require.NoError(t, err)
				baseline := uninterruptedAudioPacketsWithPipeline(t, ctx, source, test.target, native.(gst.Pipeline))
				cert, key := nodeSignerForTest(t)
				mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "node.test"}}
				var got []audioPacket
				var completed [][]byte
				var tail bool
				tr := mm.newStreamTranscoder(ctx, test.target, cert, key, func(_ any, data []byte, audioOnly bool) {
					completed = append(completed, data)
					tail = tail || audioOnly
				})
				for i, data := range source {
					require.NoError(t, tr.Feed(data, i))
				}
				require.NoError(t, tr.Close())
				require.True(t, tail)
				for _, data := range completed {
					verification, err := muxl.RunMuxlVerify(ctx, bytes.NewReader(data))
					require.NoError(t, err)
					_, err = parseVerifiedSegments([]byte(verification))
					require.NoError(t, err)
					got = append(got, canonicalAudioPackets(t, ctx, data, test.target)...)
				}
				require.Len(t, got, len(baseline), "timestamp correction must preserve every encoded sample, including EOS")
				var maxGap int64
				for i := range got {
					require.Equal(t, baseline[i].data, got[i].data, "packet %d payload must remain unchanged", i)
					require.InDelta(t, baseline[i].pts, got[i].pts, float64(time.Second)/48000, "packet %d must preserve the native sample clock and real discontinuities", i)
					if i > 0 {
						delta := int64(got[i].pts) - int64(got[i-1].pts+got[i-1].duration)
						maxGap = max(maxGap, int64(baseline[i].pts)-int64(baseline[i-1].pts+baseline[i-1].duration))
						if gap == 0 {
							require.Zero(t, delta, "packet %d must meet its predecessor exactly", i)
						}
					}
				}
				require.Equal(t, baseline[0].pts, got[0].pts, "preserve source origin and codec lookahead")
				require.GreaterOrEqual(t, got[0].pts, uint64(299*time.Second))
				if gap > 0 {
					require.Greater(t, maxGap, int64(40*time.Millisecond), "a real discontinuity beyond the encoder tolerance must remain visible")
				}
			})
		}
	}
}

// Recontainer existing encoded samples without changing their bytes. AAC clocks
// mimic RTMP's millisecond precision; Opus receives sub-millisecond jitter.
func roundedAudioSource(t *testing.T, ctx context.Context, fixture string, gap time.Duration, internal, drift bool) [][]byte {
	t.Helper()
	original := signedAudioSourceAt(t, ctx, fixture, 300*time.Second)
	signer := newBareSegmentSigner(t)
	var source [][]byte
	for n, data := range original {
		events, err := unwrapMuxlEvents(ctx, data)
		require.NoError(t, err)
		selected, err := selectEventTracks(events, 0, "", fixture == "h264-opus-frag.mp4")
		require.NoError(t, err)
		var header bytes.Buffer
		require.NoError(t, muxl.RunMuxlWrapInit(ctx, bytes.NewReader(selected), &header))
		events, err = unwrapMuxlEvents(ctx, selected)
		require.NoError(t, err)
		cat := catalogFromEvents(events)
		audioID, ok := audioTrackID(cat)
		require.True(t, ok)
		scales := map[string]uint64{}
		for _, v := range cat.Video.Renditions {
			scales[strconv.Itoa(int(v.TrackID()))] = uint64(v.Timescale())
		}
		for _, a := range cat.Audio.Renditions {
			scales[strconv.Itoa(int(a.TrackID()))] = uint64(a.Timescale())
		}
		var audioSamples int
		for _, event := range events {
			audioSamples += len(event.Samples[strconv.Itoa(int(audioID))].Sizes)
		}
		require.Greater(t, audioSamples, 1)
		tracks := map[string]*fmp4.PartTrack{}
		for _, event := range events {
			for id, track := range event.Tracks {
				samples := event.Samples[id]
				if len(samples.Sizes) == 0 {
					continue
				}
				ticks := event.FirstDecodeTimes[id]
				scale := scales[id]
				quantize := func(ticks uint64) uint64 {
					// Sub-millisecond packet-duration corrections can accumulate beyond
					// native resync tolerance even though every encoded sample is intact.
					if drift {
						origin := uint64(300) * scale
						return origin + (ticks-origin)*31/32
					}
					if fixture == "ertmp-red-h264.mp4" {
						return ticks * 1000 / scale * scale / 1000
					}
					return ticks + uint64((ticks/960)%2)*16
				}
				out := tracks[id]
				if out == nil {
					start := ticks
					if id == strconv.Itoa(int(audioID)) {
						start = quantize(ticks)
					}
					if n > 0 {
						start += uint64(gap) * scale / uint64(time.Second)
					}
					trackID, err := strconv.Atoi(id)
					require.NoError(t, err)
					out = &fmp4.PartTrack{ID: trackID, BaseTime: start}
					tracks[id] = out
				}
				for i, offset := range samples.Offsets {
					duration := samples.Durations[i]
					if id == strconv.Itoa(int(audioID)) {
						duration = uint32(quantize(ticks+uint64(duration)) - quantize(ticks))
						if internal && n == 0 && len(out.Samples) == audioSamples/2 {
							duration += uint32(uint64(gap) * scale / uint64(time.Second))
						}
					}
					out.Samples = append(out.Samples, &fmp4.Sample{Payload: track[offset : offset+uint64(samples.Sizes[i])], Duration: duration, PTSOffset: samples.CtsOffsets[i]})
					ticks += uint64(samples.Durations[i])
				}
			}
		}
		part := fmp4.Part{}
		for _, track := range tracks {
			part.Tracks = append(part.Tracks, track)
		}
		sort.Slice(part.Tracks, func(i, j int) bool { return part.Tracks[i].ID < part.Tracks[j].ID })
		var input seekablebuffer.Buffer
		_, err = input.Write(header.Bytes())
		require.NoError(t, err)
		require.NoError(t, part.Marshal(&input))
		output := make(chan *muxl.MuxlEvent, 8)
		done := make(chan error, 1)
		go func() {
			done <- signer.SignSegmentStream(ctx, bytes.NewReader(input.Bytes()), output, nil)
			close(output)
		}()
		var signed []byte
		for event := range output {
			if event.Type == "signed-segment" {
				signed = append(signed, concatTracksSorted(event.Tracks)...)
			}
		}
		require.NoError(t, <-done)
		signedEvents, err := unwrapMuxlEvents(ctx, signed)
		require.NoError(t, err)
		signedCatalog := catalogFromEvents(signedEvents)
		require.NotNil(t, signedCatalog.Video)
		require.NotNil(t, signedCatalog.Audio)
		var videoCount, audioCount int
		for _, event := range signedEvents {
			for id, count := range event.SampleCounts {
				if id == strconv.Itoa(int(audioID)) {
					audioCount += int(count)
				} else {
					videoCount += int(count)
				}
			}
		}
		require.Positive(t, videoCount)
		require.Positive(t, audioCount)
		source = append(source, signed)
	}
	require.GreaterOrEqual(t, len(source), 2)
	return source
}
