package media

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"stream.place/streamplace/pkg/log"
	"stream.place/streamplace/pkg/muxl"
	"stream.place/streamplace/test/remote"
)

func TestMediaDataParserAV1(t *testing.T) {
	withNoGSTLeaks(t, func() {
		fixture, err := os.ReadFile(getFixture("av1-aac-frag.mp4"))
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		metadata, err := ParseSegmentMediaData(ctx, fixture)
		require.NoError(t, err)
		require.Len(t, metadata.Video, 1)
		require.Len(t, metadata.Audio, 1)
		encoded, err := json.Marshal(metadata)
		require.NoError(t, err)
		require.Contains(t, string(encoded), `"codec":"av1"`)
		require.Contains(t, string(encoded), `"codec":"aac"`)
		require.Equal(t, 160, metadata.Video[0].Width)
		require.Equal(t, 120, metadata.Video[0].Height)
		require.Greater(t, metadata.Duration, int64(0))
	})
}

func TestMediaDataParserMeasuresPublicationBatch(t *testing.T) {
	for _, offset := range []time.Duration{0, 250 * time.Millisecond} {
		t.Run(offset.String(), func(t *testing.T) {
			withNoGSTLeaks(t, func() {
				flat := runSynthPipeline(t, t.Context(), fmt.Sprintf(
					"videotestsrc num-buffers=30 ! video/x-raw,width=160,height=120,framerate=30/1 ! x264enc tune=zerolatency speed-preset=ultrafast ! h264parse ! mp4mux name=mux ! appsink name=sink audiotestsrc num-buffers=50 samplesperbuffer=960 timestamp-offset=%d ! audio/x-raw,rate=48000,channels=1 ! audioconvert ! opusenc ! mux.", offset))
				metadata, err := ParseSegmentMediaData(t.Context(), flat)
				require.NoError(t, err)
				require.InDelta(t, time.Second+offset, metadata.BatchDuration, float64(40*time.Millisecond),
					"publication timing includes the later audio track's offset and final sample")
			})
		})
	}
}

func TestMediaDataParserPreservesAllTrackIDs(t *testing.T) {
	withNoGSTLeaks(t, func() {
		fixture, err := os.ReadFile(getFixture("h264-opus-frag.mp4"))
		require.NoError(t, err)
		base, err := muxl.RunMuxlCanonicalize(t.Context(), fixture, map[uint32]uint32{1: 7, 2: 42})
		require.NoError(t, err)
		events, err := unwrapMuxlEvents(t.Context(), base)
		require.NoError(t, err)
		_, tracks := catalogAndTracks(events)
		other, err := muxl.RunMuxlCanonicalize(t.Context(), renditionMP4(t, 640, 480), map[uint32]uint32{1: 20})
		require.NoError(t, err)
		events, err = unwrapMuxlEvents(t.Context(), other)
		require.NoError(t, err)
		_, extra := catalogAndTracks(events)
		tracks["20"] = extra["20"]
		var flat bytes.Buffer
		require.NoError(t, muxl.RunMuxlWrap(t.Context(), bytes.NewReader(concatTracksByID(tracks)), "flat", &flat))
		metadata, err := ParseSegmentMediaData(t.Context(), flat.Bytes())
		require.NoError(t, err)
		require.Len(t, metadata.Video, 2)
		require.Len(t, metadata.Audio, 1)
		encoded, err := json.Marshal(metadata)
		require.NoError(t, err)
		var got struct {
			Video []struct {
				TrackID uint32 `json:"trackId"`
				Codec   string `json:"codec"`
			} `json:"video"`
			Audio []struct {
				TrackID uint32 `json:"trackId"`
				Codec   string `json:"codec"`
			} `json:"audio"`
		}
		require.NoError(t, json.Unmarshal(encoded, &got))
		require.Equal(t, uint32(20), got.Video[0].TrackID, "larger source video is primary regardless of pad or track ID order")
		require.Equal(t, uint32(7), got.Video[1].TrackID)
		require.Equal(t, "h264", got.Video[0].Codec)
		require.Equal(t, uint32(42), got.Audio[0].TrackID)
		require.Equal(t, "opus", got.Audio[0].Codec)
	})
}

func TestMediaDataParser(t *testing.T) {
	segmentsWithoutBFrames := []string{
		remote.RemoteFixture("d63d26050db9a60c0944b4c2e2b1d052c4350a2a8a877324c7b0b7e7a0c1ae27/bframe-false-positive.mp4"),
		getFixture("sample-segment.mp4"),
		remote.RemoteFixture("604bebf51c97f27aa07a8952462ac9885dd963f7a88375154217f59db32e1573/2025-11-18T01-10-56-292Z-signed-segment.mp4"),
		remote.RemoteFixture("1083078df7b88bf2b658a9895f54a733879a665e8b8748706ab1a0f7ab15efdb/2026-04-11T22-15-30-041Z-muxl_segment_input.fmp4"),
		remote.RemoteFixture("82d20ee62b02f1c3a727b3001f1fa939afb757f9f205fa438d7b5753e1253eef/2026-04-11T22-39-41-861Z-packetize-input-019d7eb3-6f24-776c-ba1b-2f909a2379d7.mp4"),
		remote.RemoteFixture("45c56c6e95c5babfaa86f1d07e94e0c54faefc8e4d334995d2fd5fcc9dbb4de2/segment-with-edit-list-bug.mp4"),
	}
	withNoGSTLeaks(t, func() {
		for _, segment := range segmentsWithoutBFrames {
			// Open input file
			inputFile, err := os.Open(segment)
			require.NoError(t, err)
			defer inputFile.Close()
			bs, err := io.ReadAll(inputFile)
			require.NoError(t, err)

			ctx := log.WithDebugValue(context.Background(), map[string]map[string]int{"GStreamerFunc": {"ParseSegmentMediaData": 9}})
			mediaData, err := ParseSegmentMediaData(ctx, bs)
			require.NoError(t, err)
			require.NotNil(t, mediaData)
			require.False(t, mediaData.Video[0].BFrames, "Video should not have BFrames")
			require.Greater(t, mediaData.Duration, int64(0), "Video duration should not be empty")
		}
	})
}

func TestMediaDataParserBFrames(t *testing.T) {
	withNoGSTLeaks(t, func() {
		inputFile, err := os.Open(remote.RemoteFixture("5ea6c4491bade0cdcad3770aa0b63b2cd7a580e233ee320d5bc2282503b26491/segment-with-bframes.mp4"))
		require.NoError(t, err)
		defer inputFile.Close()
		bs, err := io.ReadAll(inputFile)
		require.NoError(t, err)

		ctx := log.WithDebugValue(context.Background(), map[string]map[string]int{"GStreamerFunc": {"ParseSegmentMediaData": 9}})
		mediaData, err := ParseSegmentMediaData(ctx, bs)
		require.NoError(t, err)
		require.NotNil(t, mediaData)
		require.True(t, mediaData.Video[0].BFrames, "Video should have BFrames")
		require.Greater(t, mediaData.Duration, int64(0), "Video duration should not be empty")
	})
}

func TestMediaDataParserVideoHeaderWithNoVideo(t *testing.T) {
	withNoGSTLeaks(t, func() {
		inputFile, err := os.Open(remote.RemoteFixture("0aa38ed08bb6b6b0ae5f4891a97244717e2c952d5ca878e34450729770f7ca53/2025-11-16T23-05-04-512Z-converge-segment-did-key-zQ3shkzEYN8UrJoRAGS6pgPodXjdg8kF2fXQNGfJhpg3x4KJT.mp4"))
		require.NoError(t, err)
		defer inputFile.Close()
		bs, err := io.ReadAll(inputFile)
		require.NoError(t, err)

		ctx := log.WithDebugValue(context.Background(), map[string]map[string]int{"GStreamerFunc": {"ParseSegmentMediaData": 9}})
		mediaData, err := ParseSegmentMediaData(ctx, bs)
		require.ErrorContains(t, err, "no video in segment")
		require.Nil(t, mediaData)
	})
}

// Video and audio finish on separate native threads. Concurrent real parses
// must retain both tracks and their metadata through EOS.
func TestMediaDataParserConcurrentEOS(t *testing.T) {
	var logs logCapture
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	previousVerbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "3"))
	t.Cleanup(func() { require.NoError(t, flag.Set("v", previousVerbosity)) })
	withNoGSTLeaks(t, func() {
		data, err := os.ReadFile(getFixture("sample-segment.mp4"))
		require.NoError(t, err)
		group, ctx := errgroup.WithContext(t.Context())
		group.SetLimit(4)
		for range 16 {
			group.Go(func() error {
				metadata, err := ParseSegmentMediaData(ctx, data)
				if err != nil {
					return err
				}
				if len(metadata.Video) != 1 || len(metadata.Audio) != 1 || metadata.Duration <= 0 {
					return fmt.Errorf("EOS lost media tracks or duration: %+v", metadata)
				}
				if metadata.Video[0].Width <= 0 || metadata.Video[0].Height <= 0 || metadata.Audio[0].Rate <= 0 || metadata.Audio[0].Channels <= 0 {
					return fmt.Errorf("EOS lost video dimensions or audio configuration: %+v", metadata)
				}
				return nil
			})
		}
		require.NoError(t, group.Wait())
	})
	// The fixture's initial zero-duration picture warns once per parse.
	require.Equal(t, 16, strings.Count(logs.String(), "no duration found for track"))
	require.NotContains(t, logs.String(), "level=ERROR")
}
