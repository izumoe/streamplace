package transcoder

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/gstinit"
	"stream.place/streamplace/pkg/renditions"
)

func TestEncodeRenditions(t *testing.T) {
	gstinit.InitGST()
	for _, codec := range []string{"opus", "aac"} {
		t.Run(codec, func(t *testing.T) {
			source := encodeFixture(t, "h264-opus-frag.mp4")
			if codec == "aac" {
				source = convertFixture(t, source, "-c:v", "copy", "-c:a", "aac")
			}
			before := probeEncoded(t, source)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			outputs, err := encode(ctx, source, []renditions.Profile{
				{Name: "small", Width: 160, Height: 120, Bitrate: 200_000, Profile: "high"},
				{Name: "smaller", Width: 80, Height: 60, Bitrate: 100_000, FPS: 15, FPSDen: 1, Profile: "baseline"},
			})
			require.NoError(t, err)
			require.Len(t, outputs, 2)
			for i, output := range outputs {
				after := probeEncoded(t, output)
				require.Len(t, after.Streams, 2)
				video, audio := after.Streams[0], after.Streams[1]
				require.Equal(t, "h264", video.CodecName)
				require.Equal(t, [2]int{160 >> i, 120 >> i}, [2]int{video.Width, video.Height})
				require.Zero(t, video.BFrames, "live renditions must not add frame reordering latency")
				require.Equal(t, codec, audio.CodecName)
				require.Equal(t, before.audioHashes(), after.audioHashes(), "compressed audio packets pass through unchanged")
				wantFrames, err := strconv.Atoi(before.Streams[0].Packets)
				require.NoError(t, err)
				if i == 1 {
					wantFrames /= 2
					require.Equal(t, "15/1", video.FrameRate)
				}
				require.Equal(t, strconv.Itoa(wantFrames), video.Packets)
				require.Contains(t, string(output[:min(len(output), 1024)]), "moov")
				require.Contains(t, string(output), "moof", "output is fragmented MP4")
			}
		})
	}
}

func TestEncodePreservesBFrameTail(t *testing.T) {
	gstinit.InitGST()
	source := convertFixture(t, encodeFixture(t, "5sec.mp4"), "-c", "copy")
	before := probeEncoded(t, source)
	require.Positive(t, before.Streams[0].BFrames)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	outputs, err := encode(ctx, source, []renditions.Profile{{Name: "small", Width: 160, Height: 90, Bitrate: 200_000, Profile: "high"}})
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	after := probeEncoded(t, outputs[0])
	require.Equal(t, before.Streams[0].Packets, after.Streams[0].Packets, "decoder drains every reordered source frame at EOS")
	require.Equal(t, before.audioHashes(), after.audioHashes())
}

func TestEncodeCancellation(t *testing.T) {
	gstinit.InitGST()
	source := encodeFixture(t, "5sec.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	outputs, err := encode(ctx, source, []renditions.Profile{{Name: "large", Width: 1920, Height: 1080, Bitrate: 6_000_000, Profile: "high"}})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, outputs, "canceled jobs never return partial renditions")
	require.Less(t, time.Since(started), 3*time.Second)
}

func TestEncodeInvalidMedia(t *testing.T) {
	gstinit.InitGST()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	outputs, err := encode(ctx, []byte("invalid mp4"), []renditions.Profile{{Name: "small", Width: 160, Height: 120, Bitrate: 200_000, Profile: "high"}})
	require.Error(t, err)
	require.NotErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, outputs)
}

func TestRenditionBufferLimit(t *testing.T) {
	output := renditionBuffer{limit: 4}
	n, err := output.Write([]byte("1234"))
	require.NoError(t, err)
	require.Equal(t, 4, n)
	n, err = output.Write([]byte("5"))
	require.Error(t, err)
	require.Zero(t, n)
	require.Equal(t, "1234", output.String(), "overflow bytes are never retained")
}

type encodedProbe struct {
	Streams []struct {
		CodecName string `json:"codec_name"`
		CodecType string `json:"codec_type"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		BFrames   int    `json:"has_b_frames"`
		FrameRate string `json:"r_frame_rate"`
		Packets   string `json:"nb_read_packets"`
	} `json:"streams"`
	Packets []struct {
		Stream int    `json:"stream_index"`
		Hash   string `json:"data_hash"`
	} `json:"packets"`
}

func (p encodedProbe) audioHashes() []string {
	var hashes []string
	for _, packet := range p.Packets {
		if p.Streams[packet.Stream].CodecType == "audio" {
			hashes = append(hashes, packet.Hash)
		}
	}
	return hashes
}

func encodeFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", name))
	require.NoError(t, err)
	return data
}

func convertFixture(t *testing.T, source []byte, codecArgs ...string) []byte {
	t.Helper()
	input := filepath.Join(t.TempDir(), "source.mp4")
	require.NoError(t, os.WriteFile(input, source, 0o600))
	args := append([]string{"-v", "error", "-i", input}, codecArgs...)
	args = append(args, "-movflags", "+frag_keyframe+empty_moov", "-f", "mp4", "pipe:1")
	return mediaTool(t, nil, "ffmpeg", args...)
}

func probeEncoded(t *testing.T, source []byte) encodedProbe {
	t.Helper()
	data := mediaTool(t, source, "ffprobe", "-v", "error", "-count_packets", "-show_streams", "-show_packets", "-show_data_hash", "sha256", "-of", "json", "pipe:0")
	var probe encodedProbe
	require.NoError(t, json.Unmarshal(data, &probe))
	return probe
}

func mediaTool(t *testing.T, source []byte, name string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(source)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH=")
	var errors bytes.Buffer
	cmd.Stderr = &errors
	data, err := cmd.Output()
	require.NoError(t, err, errors.String())
	return data
}
