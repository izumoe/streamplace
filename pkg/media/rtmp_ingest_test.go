package media

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abema/go-mp4"
	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/codecs"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/av1"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/gstinit"
	"stream.place/streamplace/pkg/muxl"
)

type rtmpFixture struct {
	video       RTMPVideoTrack
	frames      []*RTMPVideoData
	audio       *gortmplib.Track
	audioFrames []*RTMPAACData
}

func rtmpAVConfig(t *testing.T, data []byte, sampleEntry, config mp4.BoxType) mp4.IBox {
	t.Helper()
	boxes, err := mp4.ExtractBoxWithPayload(bytes.NewReader(data), nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
		mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), sampleEntry, config,
	})
	require.NoError(t, err)
	require.Len(t, boxes, 1)
	return boxes[0].Payload
}

// Extract encoded samples through muxl's existing per-sample tables. No decoder
// or re-encoder sits between the fixture and the ingest boundary under test.
func readRTMPFixture(t *testing.T, name string) rtmpFixture {
	t.Helper()
	fixture, err := os.ReadFile(getFixture(name))
	require.NoError(t, err)
	events, err := segmentMuxlEvents(t.Context(), fixture)
	require.NoError(t, err)
	cat, _ := catalogAndTracks(events)
	require.NotNil(t, cat)
	require.Len(t, cat.Video.Renditions, 1)
	var video muxl.MuxlVideoConfig
	for _, v := range cat.Video.Renditions {
		video = v
	}
	var result rtmpFixture
	var av1Config mp4.Av1C
	switch {
	case strings.HasPrefix(video.Codec, "avc1"):
		avcc := rtmpAVConfig(t, fixture, mp4.BoxTypeAvc1(), mp4.BoxTypeAvcC()).(*mp4.AVCDecoderConfiguration)
		result.video.Track = &gortmplib.Track{Codec: &codecs.H264{
			SPS: avcc.SequenceParameterSets[0].NALUnit,
			PPS: avcc.PictureParameterSets[0].NALUnit,
		}}
	case strings.HasPrefix(video.Codec, "av01"):
		av1Config = *rtmpAVConfig(t, fixture, mp4.BoxTypeAv01(), mp4.BoxTypeAv1C()).(*mp4.Av1C)
		result.video.Track = &gortmplib.Track{Codec: &codecs.AV1{}}
	default:
		t.Fatalf("unexpected fixture codec %s", video.Codec)
	}
	var audio muxl.MuxlAudioConfig
	if cat.Audio != nil {
		for _, a := range cat.Audio.Renditions {
			if isAACCodec(a.Codec) {
				audio = a
				result.audio = &gortmplib.Track{Codec: &codecs.MPEG4Audio{Config: &mpeg4audio.AudioSpecificConfig{
					Type: 2, SampleRate: int(a.SampleRate), ChannelConfig: uint8(a.NumberOfChannels),
				}}}
			}
		}
	}
	videoKey, audioKey := fmt.Sprint(video.TrackID()), fmt.Sprint(audio.TrackID())
	var videoBase, audioBase uint64
	haveVideo, haveAudio := false, false
	for _, event := range events {
		if event.Type != "segment" {
			continue
		}
		if samples, ok := event.Samples[videoKey]; ok {
			dts := event.FirstDecodeTimes[videoKey]
			if !haveVideo {
				videoBase, haveVideo = dts, true
			}
			for i, offset := range samples.Offsets {
				data := event.Tracks[videoKey][offset : offset+uint64(samples.Sizes[i])]
				var au [][]byte
				if _, ok := result.video.Track.Codec.(*codecs.AV1); ok {
					var tu av1.Bitstream
					require.NoError(t, tu.Unmarshal(data))
					var config av1.Bitstream
					for _, obu := range tu {
						if (obu[0]>>3)&15 == 1 {
							config = append(config, obu)
							continue
						}
						au = append(au, obu)
					}
					if len(config) > 0 {
						av1Config.ConfigOBUs, err = config.Marshal()
						require.NoError(t, err)
					}
				} else {
					var nalus h264.AVCC
					require.NoError(t, nalus.Unmarshal(data))
					for _, nal := range nalus {
						if typ := nal[0] & 31; typ >= 1 && typ <= 5 {
							au = append(au, nal)
						}
					}
				}
				stamp := time.Duration(dts-videoBase) * time.Second / time.Duration(video.Timescale())
				cts := time.Duration(samples.CtsOffsets[i]) * time.Second / time.Duration(video.Timescale())
				result.frames = append(result.frames, &RTMPVideoData{AU: au, DTS: stamp, PTS: stamp + cts})
				dts += uint64(samples.Durations[i])
			}
		}
		if result.audio != nil {
			samples := event.Samples[audioKey]
			dts := event.FirstDecodeTimes[audioKey]
			if !haveAudio {
				audioBase, haveAudio = dts, true
			}
			for i, offset := range samples.Offsets {
				result.audioFrames = append(result.audioFrames, &RTMPAACData{
					AU:  event.Tracks[audioKey][offset : offset+uint64(samples.Sizes[i])],
					PTS: time.Duration(dts-audioBase) * time.Second / time.Duration(audio.Timescale()),
				})
				dts += uint64(samples.Durations[i])
			}
		}
	}
	if _, ok := result.video.Track.Codec.(*codecs.AV1); ok {
		require.NotEmpty(t, av1Config.ConfigOBUs, "AV1 sequence header must be carried only by configuration")
		var config bytes.Buffer
		_, err := mp4.Marshal(&config, &av1Config, mp4.Context{})
		require.NoError(t, err)
		result.video.AV1Config = config.Bytes()
	}
	require.NotEmpty(t, result.frames)
	return result
}

func rtmpSessionForFixtures(t *testing.T, audio rtmpFixture, videos ...rtmpFixture) *RTMPSession {
	t.Helper()
	session := &RTMPSession{AudioTrack: audio.audio, MediaSigner: newBareSegmentSigner(t)}
	var events []any
	for id, fixture := range videos {
		session.VideoTracks = append(session.VideoTracks, fixture.video)
		for _, frame := range fixture.frames {
			packet := *frame
			packet.TrackID = id
			if _, ok := fixture.video.Track.Codec.(*codecs.AV1); ok {
				packet.PTS = packet.DTS + 33*time.Millisecond
			}
			events = append(events, &packet)
		}
	}
	for _, frame := range audio.audioFrames {
		events = append(events, frame)
	}
	stamp := func(event any) time.Duration {
		switch event := event.(type) {
		case *RTMPVideoData:
			return event.DTS
		case *RTMPAACData:
			return event.PTS
		}
		panic("unexpected test event")
	}
	sort.SliceStable(events, func(i, j int) bool { return stamp(events[i]) < stamp(events[j]) })
	session.EventChan = make(chan any, len(events))
	for _, event := range events {
		session.EventChan <- event
	}
	return session
}

func TestRTMPDirectIngestSignsOriginalVideoTracks(t *testing.T) {
	gstinit.InitGST()
	av1Fixture := readRTMPFixture(t, "av1-aac-frag.mp4")
	h264Fixture := readRTMPFixture(t, "h264-opus-frag.mp4")
	for _, tc := range []struct {
		name   string
		videos []rtmpFixture
	}{
		{"H264", []rtmpFixture{h264Fixture}},
		{"AV1 sequence-start config", []rtmpFixture{av1Fixture}},
		{"mixed H264 AV1", []rtmpFixture{h264Fixture, av1Fixture}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			session := rtmpSessionForFixtures(t, av1Fixture, tc.videos...)
			close(session.EventChan)
			mm := &MediaManager{cli: &config.CLI{}, bus: bus.NewBus()}
			var signed [][]byte
			require.NoError(t, mm.rtmpIngestSession(ctx, session, func(_ context.Context, segment []byte) error {
				signed = append(signed, segment)
				return nil
			}))
			require.NotEmpty(t, signed, "clean EOF drains the final signed GoP")
			counts := make(map[string]uint32)
			for _, segment := range signed {
				_, err := ValidateMP4MediaC2PA(ctx, segment)
				require.NoError(t, err, "all original video and AAC signatures verify")
				unwrapped, err := unwrapMuxlEvents(ctx, segment)
				require.NoError(t, err)
				cat, _ := catalogAndTracks(unwrapped)
				require.NotNil(t, cat)
				require.NotNil(t, cat.Video)
				for _, v := range cat.Video.Renditions {
					require.Positive(t, v.TrackID())
					require.LessOrEqual(t, v.TrackID(), uint32(len(tc.videos)))
					require.Less(t, v.TrackID(), uint32(100))
					require.Equal(t, strings.HasPrefix(v.Codec, "av01"), tc.videos[v.TrackID()-1].video.AV1Config != nil)
					if strings.HasPrefix(v.Codec, "av01") {
						var init, description bytes.Buffer
						require.NoError(t, muxl.RunMuxlWrapInit(ctx, bytes.NewReader(segment), &init))
						_, err := mp4.Marshal(&description, rtmpAVConfig(t, init.Bytes(), mp4.BoxTypeAv01(), mp4.BoxTypeAv1C()), mp4.Context{})
						require.NoError(t, err)
						require.Equal(t, tc.videos[v.TrackID()-1].video.AV1Config, description.Bytes())
						for _, event := range unwrapped {
							for _, cts := range event.Samples[strconv.FormatUint(uint64(v.TrackID()), 10)].CtsOffsets {
								require.InDelta(t, float64(33*time.Millisecond), float64(cts)*float64(time.Second)/float64(v.Timescale()), float64(time.Microsecond))
							}
						}
					}
				}
				for _, event := range unwrapped {
					for id, count := range event.SampleCounts {
						counts[id] += count
					}
				}
			}
			for id, fixture := range tc.videos {
				require.EqualValues(t, len(fixture.frames), counts[strconv.Itoa(id+1)], "track %d loses no encoded frames", id)
			}
			require.EqualValues(t, len(av1Fixture.audioFrames), counts[strconv.Itoa(len(tc.videos)+1)], "source AAC is copied intact")
		})
	}
}

func TestRTMPDirectIngestPublishesPlayableMultitrackBatches(t *testing.T) {
	gstinit.InitGST()
	red := readRTMPFixture(t, "ertmp-red-h264.mp4")
	blue := readRTMPFixture(t, "ertmp-blue-h264.mp4")
	green := readRTMPFixture(t, "ertmp-green-av1.mp4")
	unequal := readRTMPFixture(t, "h264-opus-frag.mp4")
	tail := red
	tail.audioFrames = append([]*RTMPAACData(nil), red.audioFrames...)
	end := red.audioFrames[len(red.audioFrames)-1].PTS + 1024*time.Second/48000
	for i, frame := range red.audioFrames[:24] {
		packet := *frame
		packet.PTS = end + time.Duration(i)*1024*time.Second/48000
		tail.audioFrames = append(tail.audioFrames, &packet)
	}
	missingTail := red
	missingTail.audioFrames = red.audioFrames[:len(red.audioFrames)/2]
	for _, tc := range []struct {
		name   string
		audio  rtmpFixture
		videos []rtmpFixture
		error  string
	}{
		{"three tracks", red, []rtmpFixture{red, blue, green}, ""},
		{"unequal video cadence", red, []rtmpFixture{red, unequal, green}, ""},
		{"AAC tail", tail, []rtmpFixture{red, blue, green}, ""},
		{"AAC ends before video", missingTail, []rtmpFixture{red, blue, green}, "no audio in segment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertRTMPPlayableBatches(t, tc.audio, tc.videos, tc.error)
		})
	}
}

func assertRTMPPlayableBatches(t *testing.T, audio rtmpFixture, videos []rtmpFixture, wantError string) {
	t.Helper()
	session := rtmpSessionForFixtures(t, audio, videos...)
	close(session.EventChan)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	signCtx, flush := context.WithCancel(ctx)
	defer flush()
	var raw []*muxl.MuxlEvent
	var batches [][]byte
	signer, done, err := muxlSignSegmentElem(signCtx, &config.CLI{}, func(ctx context.Context, input io.Reader, output chan *muxl.MuxlEvent) error {
		events := make(chan *muxl.MuxlEvent, 16)
		signed := make(chan error, 1)
		go func() {
			signed <- session.MediaSigner.SignSegmentStream(ctx, input, events)
			close(events)
		}()
		for event := range events {
			raw = append(raw, event)
			output <- event
		}
		return <-signed
	}, len(session.VideoTracks), func(_ context.Context, segment []byte) error {
		batches = append(batches, segment)
		return nil
	})
	require.NoError(t, err)
	pipeline, sources, err := newRTMPDirectPipeline(session, signer)
	require.NoError(t, err)
	err = runRTMPDirectPipeline(ctx, pipeline, session, sources)
	flush()
	<-done
	require.NoError(t, err)
	require.NotEmpty(t, batches)
	for _, event := range raw {
		sync := map[string][]uint32{}
		for id, samples := range event.Samples {
			sync[id] = samples.SyncIndices
		}
		t.Logf("signer event type=%s number=%d counts=%v first=%v duration=%v sync=%v", event.Type, event.Number, event.SampleCounts, event.FirstDecodeTimes, event.Durations, sync)
	}
	counts := map[string]uint32{}
	for _, event := range raw {
		for id, count := range event.SampleCounts {
			counts[id] += count
		}
	}
	for i, fixture := range videos {
		require.EqualValues(t, len(fixture.frames), counts[strconv.Itoa(i+1)], "every input video frame reaches the signer")
	}
	require.EqualValues(t, len(audio.audioFrames), counts[strconv.Itoa(len(videos)+1)], "every input AAC packet reaches the signer")
	for i, batch := range batches {
		result, err := ValidateMP4Media(ctx, batch)
		if wantError != "" && i == len(batches)-1 {
			require.ErrorContains(t, err, wantError, "an incomplete EOF batch still passes through ordinary admission")
			continue
		}
		require.NoError(t, err, "batch %d must contain playable video and audio", i)
		require.NotEmpty(t, result.MediaData.Video)
		require.NotEmpty(t, result.MediaData.Audio)
	}
	combined := bytes.Join(batches, nil)
	for _, event := range raw {
		for id, track := range event.Tracks {
			require.Equal(t, 1, bytes.Count(combined, track), "track %s event %d preserves its exact signed bytes once", id, event.Number)
		}
	}
}

func TestRTMPDirectIngestPublishesMultitrackBeforeEOF(t *testing.T) {
	gstinit.InitGST()
	red := readRTMPFixture(t, "ertmp-red-h264.mp4")
	blue := readRTMPFixture(t, "ertmp-blue-h264.mp4")
	green := readRTMPFixture(t, "ertmp-green-av1.mp4")
	session := rtmpSessionForFixtures(t, red, red, blue, green)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	mm := &MediaManager{cli: &config.CLI{}, bus: bus.NewBus()}
	var first []byte
	err := mm.rtmpIngestSession(ctx, session, func(_ context.Context, segment []byte) error {
		if first == nil {
			first = segment
			cancel()
		}
		return nil
	})
	require.ErrorIs(t, err, context.Canceled, "a complete live batch must publish while packet input remains open")
	require.NotEmpty(t, first)
	result, err := ValidateMP4Media(t.Context(), first)
	require.NoError(t, err)
	require.Len(t, result.MediaData.Video, 3)
	require.Len(t, result.MediaData.Audio, 1)
}

func TestRTMPMultitrackCompletionAndExtraction(t *testing.T) {
	gstinit.InitGST()
	red := readRTMPFixture(t, "ertmp-red-h264.mp4")
	blue := readRTMPFixture(t, "ertmp-blue-h264.mp4")
	green := readRTMPFixture(t, "ertmp-green-av1.mp4")
	session := rtmpSessionForFixtures(t, red, red, blue, green)
	close(session.EventChan)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "test.example.com"}, bus: bus.NewBus()}
	var sources [][]byte
	require.NoError(t, mm.rtmpIngestSession(ctx, session, func(_ context.Context, segment []byte) error {
		sources = append(sources, segment)
		return nil
	}))
	require.GreaterOrEqual(t, len(sources), 2)
	t.Run("MoQ track extraction", func(t *testing.T) {
		counts := map[string]uint32{}
		for _, source := range sources {
			original := bytes.Clone(source)
			events, err := unwrapMuxlEvents(ctx, source)
			require.NoError(t, err)
			expected := map[string][]byte{}
			for _, event := range events {
				for id, data := range event.Tracks {
					expected[id] = append(expected[id], data...)
					counts[id] += event.SampleCounts[id]
				}
			}
			tracks, err := MuxlTracks(ctx, source)
			require.NoError(t, err)
			require.Equal(t, 4, len(tracks), "all source videos and common AAC reach MoQ")
			for id, data := range expected {
				name := "video" + id
				if id == "4" {
					name = "audio4"
				}
				require.True(t, bytes.Equal(data, tracks[name]), "track %s preserves every signed fragment in order", id)
			}
			require.True(t, bytes.Equal(original, source), "extraction leaves the canonical source unchanged")
			_, err = ValidateMP4MediaC2PA(ctx, source)
			require.NoError(t, err)
		}
		for i, fixture := range []rtmpFixture{red, blue, green} {
			require.EqualValues(t, len(fixture.frames), counts[strconv.Itoa(i+1)])
		}
		require.EqualValues(t, len(red.audioFrames), counts["4"])
	})
	t.Run("continuous AAC completion", func(t *testing.T) {
		var logs logCapture
		previousLogger := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
		t.Cleanup(func() { slog.SetDefault(previousLogger) })
		previousVerbosity := flag.Lookup("v").Value.String()
		require.NoError(t, flag.Set("v", "1"))
		t.Cleanup(func() { require.NoError(t, flag.Set("v", previousVerbosity)) })
		cert, key := nodeSignerForTest(t)
		var mu sync.Mutex
		var completed [][]byte
		encoder := mm.newStreamTranscoder(ctx, "opus", cert, key, func(_ any, segment []byte) {
			mu.Lock()
			completed = append(completed, segment)
			mu.Unlock()
		})
		for i, source := range sources {
			require.NoError(t, encoder.Feed(source, i))
		}
		require.NoError(t, encoder.Close())
		mu.Lock()
		defer mu.Unlock()
		require.Equal(t, len(sources), len(completed), "every assembled source batch gets Opus: %s", logs.String())
		for i, segment := range completed {
			require.True(t, bytes.HasPrefix(segment, sources[i]), "completion preserves all original signed bytes")
			_, err := ValidateMP4MediaC2PA(ctx, segment)
			require.NoError(t, err)
			tracks, err := MuxlTracks(ctx, segment)
			require.NoError(t, err)
			require.Len(t, tracks, 5)
			require.NotEmpty(t, tracks["audio5"], "the node adds the Opus track")
		}
		require.Empty(t, logs.String(), "healthy completion emits no errors")
	})
}

func TestRTMPDirectIngestStopsOpenInput(t *testing.T) {
	gstinit.InitGST()
	fixture := readRTMPFixture(t, "av1-aac-frag.mp4")
	for _, sinkFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("sink failure=%t", sinkFailure), func(t *testing.T) {
			var logs logCapture
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })
			previousVerbosity := flag.Lookup("v").Value.String()
			require.NoError(t, flag.Set("v", "1"))
			t.Cleanup(func() { require.NoError(t, flag.Set("v", previousVerbosity)) })
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			session := rtmpSessionForFixtures(t, fixture, fixture)
			// Leave input open after the real encoded samples. Cancellation and
			// sink failure must stop a feeder waiting for the next RTMP packet.
			mm := &MediaManager{cli: &config.CLI{}, bus: bus.NewBus()}
			sinkErr := errors.New("RTMP test sink rejected signed segment")
			count := 0
			done := make(chan error, 1)
			go func() {
				done <- mm.rtmpIngestSession(ctx, session, func(_ context.Context, segment []byte) error {
					count++
					if count == 1 {
						if sinkFailure {
							return sinkErr
						}
						cancel()
					}
					return nil
				})
			}()
			select {
			case err := <-done:
				if sinkFailure {
					require.ErrorIs(t, err, sinkErr)
					require.Contains(t, logs.String(), sinkErr.Error())
				} else {
					require.ErrorIs(t, err, context.Canceled)
				}
				require.Positive(t, count, "real encoded samples must reach the signer before cancellation")
			case <-time.After(5 * time.Second):
				t.Fatal("RTMP ingest did not stop while packet input remained open")
			}
		})
	}
}

func TestRTMPPacketsBoundHeaderAccumulation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		units [][]byte
	}{
		{"NAL count", func() [][]byte {
			units := make([][]byte, h264.MaxNALUsPerAccessUnit)
			for i := range units {
				units[i] = []byte{0x67}
			}
			return units
		}()},
		{"byte size", func() [][]byte {
			unit := make([]byte, h264.MaxAccessUnitSize/2+1)
			unit[0] = 0x67
			return [][]byte{unit}
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := &RTMPSession{
				VideoTracks: []RTMPVideoTrack{{Track: &gortmplib.Track{Codec: &codecs.H264{}}}},
				EventChan:   make(chan any, 2),
			}
			session.EventChan <- &RTMPVideoData{AU: tc.units}
			session.EventChan <- &RTMPVideoData{AU: tc.units}
			close(session.EventChan)
			// Rejection happens before any picture can be sent to a native
			// source. Repeated headers must not bypass the reader's AU bounds.
			err := feedRTMPPackets(t.Context(), session, nil)
			require.ErrorContains(t, err, "H264 pending headers exceed")
		})
	}
}
