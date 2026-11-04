package media

import (
	"bytes"
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4/seekablebuffer"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/muxl"
)

func TestStreamTranscoderShortSourceDrains(t *testing.T) {
	for _, test := range []struct{ fixture, target string }{
		{"ertmp-red-h264.mp4", "opus"},
		{"h264-opus-frag.mp4", "aac"},
	} {
		t.Run(test.target, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			ms := audioSignerForTest(t)
			source := shortAudioSourceForTest(t, ctx, ms, test.fixture)
			want := uninterruptedAudioPackets(t, ctx, [][]byte{source}, test.target)
			require.NotEmpty(t, want, "EOS must flush audio shorter than codec priming")
			cert, key := nodeSignerForTest(t)
			mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
			var completed [][]byte
			tr := mm.newStreamTranscoder(ctx, test.target, cert, key, func(_ any, data []byte, tail bool) {
				appendAudioCompletion(&completed, data, tail)
			})
			t.Cleanup(func() { require.NoError(t, tr.Close()) })
			require.NoError(t, tr.Feed(source, nil))
			require.NoError(t, tr.Close(), "a valid short source can wait for codec priming and EOS instead of failing its boundary")
			require.Len(t, completed, 1)
			require.True(t, bytes.HasPrefix(completed[0], source))
			_, err := ValidateMP4MediaC2PA(ctx, completed[0])
			require.NoError(t, err)
			got := canonicalAudioPackets(t, ctx, completed[0], test.target)
			require.Len(t, got, len(want))
			for i := range want {
				require.Equal(t, want[i].data, got[i].data, "packet %d survives short-input EOS", i)
			}
		})
	}
}

func TestStreamTranscoderShortSourcesPreserveContinuousAudio(t *testing.T) {
	for _, test := range []struct{ fixture, target string }{{"ertmp-red-h264.mp4", "opus"}, {"h264-opus-frag.mp4", "aac"}} {
		for _, sequence := range []struct {
			name   string
			counts []int
		}{{"repeated tiny GoPs", []int{1, 1, 1, 1, 1, 1, 1, 1}}, {"short then normal", []int{1, -1}}} {
			for _, local := range []bool{false, true} {
				kind := "worker"
				if local {
					kind = "local"
				}
				t.Run(test.target+"/"+sequence.name+"/"+kind, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
					defer cancel()
					ms := audioSignerForTest(t)
					source := audioSourcePartsForTest(t, ctx, ms, test.fixture, sequence.counts)
					want := uninterruptedAudioPackets(t, ctx, source, test.target)
					require.NotEmpty(t, want)
					cert, key := nodeSignerForTest(t)
					mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
					type completion struct {
						token any
						data  []byte
						tail  bool
					}
					var completed []completion
					tr := mm.newStreamTranscoder(ctx, test.target, cert, key, func(token any, data []byte, tail bool) { completed = append(completed, completion{token, data, tail}) })
					t.Cleanup(func() { require.NoError(t, tr.Close()) })
					originalLabels := map[string]int{}
					for _, segment := range source {
						verification, err := muxl.RunMuxlVerify(ctx, bytes.NewReader(segment))
						require.NoError(t, err)
						assets, err := parseVerifiedSegments([]byte(verification))
						require.NoError(t, err)
						for _, asset := range assets {
							require.NotNil(t, asset.Manifest.Label)
							originalLabels[*asset.Manifest.Label] = 0
						}
						var token any
						if local {
							valid, err := ValidateMP4Media(ctx, segment)
							require.NoError(t, err)
							token = &validatedSegment{meta: valid.Meta, mediaData: valid.MediaData}
						}
						require.NoError(t, tr.Feed(segment, token))
					}
					require.NoError(t, tr.Close())
					var got []audioPacket
					nextSource := 0
					for _, output := range completed {
						verification, err := muxl.RunMuxlVerify(ctx, bytes.NewReader(output.data))
						require.NoError(t, err)
						assets, err := parseVerifiedSegments([]byte(verification))
						require.NoError(t, err)
						for _, asset := range assets {
							if asset.Manifest.Label != nil {
								if _, original := originalLabels[*asset.Manifest.Label]; original {
									originalLabels[*asset.Manifest.Label]++
								}
							}
						}
						if !output.tail {
							offset, count := 0, 0
							for nextSource < len(source) && bytes.HasPrefix(output.data[offset:], source[nextSource]) {
								offset += len(source[nextSource])
								nextSource++
								count++
							}
							require.Positive(t, count, "a completion must retain its pending sources unchanged and in order")
							if local && count > 1 {
								valid, err := ValidateMP4Media(ctx, output.data[:offset])
								require.NoError(t, err)
								require.Equal(t, valid.MediaData.Duration, output.token.(*validatedSegment).mediaData.Duration, "coalesced local source duration")
								require.Equal(t, valid.MediaData.BatchDuration, output.token.(*validatedSegment).mediaData.BatchDuration, "coalesced local publication span")
							}
						}
						got = append(got, canonicalAudioPackets(t, ctx, output.data, test.target)...)
					}
					require.Equal(t, len(source), nextSource, "every fed source must be delivered")
					for label, count := range originalLabels {
						require.Equal(t, 1, count, "original source asset %s must appear exactly once", label)
					}
					require.Len(t, got, len(want))
					for i := range want {
						require.Equal(t, want[i].data, got[i].data, "packet %d payload", i)
						require.InDelta(t, want[i].pts, got[i].pts, float64(time.Second)/48000, "packet %d PTS", i)
						require.InDelta(t, want[i].duration, got[i].duration, float64(time.Second)/48000, "packet %d duration", i)
						if i > 0 {
							require.Equal(t, got[i-1].pts+got[i-1].duration, got[i].pts, "packet %d gap or overlap", i)
						}
					}
				})
			}
		}
	}
}

func shortAudioSourceForTest(t *testing.T, ctx context.Context, signer *MediaSignerLocal, fixture string) []byte {
	t.Helper()
	return audioSourcePartsForTest(t, ctx, signer, fixture, []int{1})[0]
}

// A scene cut can produce a GoP shorter than codec lookahead. Reuse a real IDR
// and consecutive real audio packets, retiming only their lossless container.
// A negative count takes the remaining audio as one normal-sized source.
func audioSourcePartsForTest(t *testing.T, ctx context.Context, signer *MediaSignerLocal, fixture string, counts []int) [][]byte {
	t.Helper()
	original := allSignedBareSegments(t, ctx, signer, getFixture(fixture))[0]
	events, err := unwrapMuxlEvents(ctx, original)
	require.NoError(t, err)
	selected, err := selectEventTracks(events, 0, "", fixture == "h264-opus-frag.mp4")
	require.NoError(t, err)
	var header bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrapInit(ctx, bytes.NewReader(selected), &header))
	events, err = unwrapMuxlEvents(ctx, selected)
	require.NoError(t, err)
	cat := catalogFromEvents(events)
	video, ok := primaryVideoTrack(cat, 0, "")
	require.True(t, ok)
	videoID := strconv.FormatUint(uint64(video.TrackID()), 10)
	audioID, ok := audioTrackID(cat)
	require.True(t, ok)
	audioKey := strconv.FormatUint(uint64(audioID), 10)
	var audioScale uint64
	for _, audio := range cat.Audio.Renditions {
		if audio.TrackID() == audioID {
			audioScale = uint64(audio.Timescale())
		}
	}
	var picture []byte
	var pictureCTS int32
	var audio []*fmp4.Sample
	var audioStart uint64
	for _, event := range events {
		if track := event.Tracks[videoID]; len(track) > 0 && picture == nil {
			samples := event.Samples[videoID]
			start := samples.Offsets[0]
			picture = track[start : start+uint64(samples.Sizes[0])]
			pictureCTS = samples.CtsOffsets[0]
		}
		if track := event.Tracks[audioKey]; len(track) > 0 {
			samples := event.Samples[audioKey]
			if len(audio) == 0 {
				audioStart = event.FirstDecodeTimes[audioKey]
			}
			for i, start := range samples.Offsets {
				audio = append(audio, &fmp4.Sample{Payload: track[start : start+uint64(samples.Sizes[i])], Duration: samples.Durations[i], PTSOffset: samples.CtsOffsets[i]})
			}
		}
	}
	require.NotEmpty(t, picture)
	require.NotEmpty(t, audio)
	require.NotZero(t, audioScale)
	index := 0
	ticks := audioStart
	var source [][]byte
	for _, count := range counts {
		if count < 0 {
			count = len(audio) - index
		}
		require.Positive(t, count)
		require.LessOrEqual(t, index+count, len(audio))
		end := ticks
		for _, sample := range audio[index : index+count] {
			end += uint64(sample.Duration)
		}
		videoStart, videoEnd := ticks*uint64(video.Timescale())/audioScale, end*uint64(video.Timescale())/audioScale
		part := fmp4.Part{Tracks: []*fmp4.PartTrack{
			{ID: int(video.TrackID()), BaseTime: videoStart, Samples: []*fmp4.Sample{{Payload: picture, Duration: uint32(videoEnd - videoStart), PTSOffset: pictureCTS}}},
			{ID: int(audioID), BaseTime: ticks, Samples: audio[index : index+count]},
		}}
		var input seekablebuffer.Buffer
		_, err := input.Write(header.Bytes())
		require.NoError(t, err)
		require.NoError(t, part.Marshal(&input))
		output := make(chan *muxl.MuxlEvent, 4)
		done := make(chan error, 1)
		go func() { done <- signer.SignSegmentStream(ctx, bytes.NewReader(input.Bytes()), output); close(output) }()
		var signed [][]byte
		for event := range output {
			if event.Type == "signed-segment" {
				signed = append(signed, concatTracksSorted(event.Tracks))
			}
		}
		require.NoError(t, <-done)
		require.Len(t, signed, 1)
		source = append(source, signed[0])
		index += count
		ticks = end
	}
	return source
}
