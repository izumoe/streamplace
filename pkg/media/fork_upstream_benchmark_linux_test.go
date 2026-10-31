package media

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/crypto/signers"
	"stream.place/streamplace/pkg/gstinit"
	"stream.place/streamplace/pkg/ingestframe"
	"stream.place/streamplace/pkg/muxl"
)

// Both checkouts consume the same prepared files. Preparation is explicit so
// benchmark runs cannot silently regenerate their input or signing identity.
func forkBenchmarkDir(tb testing.TB) string {
	tb.Helper()
	dir := os.Getenv("STREAMPLACE_BENCH_DIR")
	if dir == "" {
		tb.Skip("set STREAMPLACE_BENCH_DIR to the shared prepared fixture directory")
	}
	return dir
}

func forkBenchmarkRead(tb testing.TB, name string) []byte {
	tb.Helper()
	data, err := os.ReadFile(filepath.Join(forkBenchmarkDir(tb), name))
	require.NoError(tb, err)
	return data
}

func forkBenchmarkSigner(tb testing.TB, role string) *MediaSignerLocal {
	tb.Helper()
	key := forkBenchmarkRead(tb, role+".key.bin")
	require.Len(tb, key, 32)
	priv, _ := secp256k1.PrivKeyFromBytes(key)
	return &MediaSignerLocal{
		StreamerName:     "benchmark-" + role,
		Signer:           priv.ToECDSA(),
		Cert:             forkBenchmarkRead(tb, role+".cert.pem"),
		PrebuiltManifest: forkBenchmarkRead(tb, role+".manifest.json"),
	}
}

func forkBenchmarkSign(ctx context.Context, signer *MediaSignerLocal, input []byte) ([][]byte, error) {
	events, err := collectMuxlEvents(func(ch chan *muxl.MuxlEvent) error {
		return signer.SignSegmentStream(ctx, bytes.NewReader(input), ch)
	})
	if err != nil {
		return nil, err
	}
	var segments [][]byte
	for _, event := range events {
		if event.Type == "signed-segment" {
			segments = append(segments, concatTracksByID(event.Tracks))
		}
	}
	return segments, nil
}

func forkBenchmarkSegments(tb testing.TB, codec string) [][]byte {
	tb.Helper()
	var segments [][]byte
	require.NoError(tb, json.Unmarshal(forkBenchmarkRead(tb, codec+".signed.json"), &segments))
	require.Len(tb, segments, 10, "10-second fixture must have ten one-second GoPs")
	return segments
}

func TestForkUpstreamBenchmarkPrepare(t *testing.T) {
	if os.Getenv("STREAMPLACE_BENCH_PREPARE") != "1" {
		t.Skip("preparation is explicit and runs once for both versions")
	}
	dir := forkBenchmarkDir(t)
	for _, role := range []string{"source", "node"} {
		_, err := os.Stat(filepath.Join(dir, role+".key.bin"))
		require.True(t, os.IsNotExist(err), "refusing to replace a shared signing identity")
	}
	gstinit.InitGST()
	for _, role := range []string{"source", "node"} {
		signer, err := NewEphemeralMediaSigner("benchmark-" + role)
		require.NoError(t, err)
		for name, data := range map[string][]byte{
			"key.bin":       ethcrypto.FromECDSA(signer.Signer.(*ecdsa.PrivateKey)),
			"cert.pem":      signer.Cert,
			"manifest.json": signer.PrebuiltManifest,
		} {
			require.NoError(t, os.WriteFile(filepath.Join(dir, role+"."+name), data, 0o600))
		}
	}
	signer := forkBenchmarkSigner(t, "source")
	for _, codec := range []string{"aac", "opus"} {
		segments, err := forkBenchmarkSign(t.Context(), signer, forkBenchmarkRead(t, codec+".mp4"))
		require.NoError(t, err)
		require.Len(t, segments, 10)
		data, err := json.Marshal(segments)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, codec+".signed.json"), data, 0o600))
	}
}

// Verification is deliberately outside the measured operation. It checks all
// source and derived signatures, encoded sample counts and media durations.
func forkBenchmarkCheck(tb testing.TB, segments, sources [][]byte, dual bool) {
	tb.Helper()
	require.Len(tb, segments, 10, "EOF must drain the complete stream")
	seconds := map[string]float64{}
	samples := map[string]int{}
	for i, segment := range segments {
		if sources != nil {
			require.True(tb, bytes.HasPrefix(segment, sources[i]), "original signed assets must remain byte-identical")
		}
		result, err := ValidateMP4Media(context.Background(), segment)
		require.NoError(tb, err)
		require.Len(tb, result.MediaData.Video, 1)
		require.Equal(tb, 1920, result.MediaData.Video[0].Width)
		require.Equal(tb, 1080, result.MediaData.Video[0].Height)
		require.NotEmpty(tb, result.MediaData.Audio)
		for _, audio := range result.MediaData.Audio {
			require.Equal(tb, 48000, audio.Rate)
		}
		var flat bytes.Buffer
		require.NoError(tb, muxl.RunMuxlWrap(context.Background(), bytes.NewReader(segment), "fmp4", &flat))
		events, err := segmentMuxlEvents(context.Background(), flat.Bytes())
		require.NoError(tb, err)
		catalog, _ := catalogAndTracks(events)
		require.NotNil(tb, catalog)
		require.NotNil(tb, catalog.Video)
		require.NotNil(tb, catalog.Audio)
		require.Len(tb, catalog.Video.Renditions, 1)
		if dual {
			require.Len(tb, catalog.Audio.Renditions, 2)
		} else {
			require.Len(tb, catalog.Audio.Renditions, 1)
		}
		codecs := map[string]string{}
		timescales := map[string]uint32{}
		for _, video := range catalog.Video.Renditions {
			require.Contains(tb, video.Codec, "avc1")
			id := strconv.FormatUint(uint64(video.TrackID()), 10)
			codecs[id], timescales[id] = "video", video.Timescale()
		}
		for _, audio := range catalog.Audio.Renditions {
			id := strconv.FormatUint(uint64(audio.TrackID()), 10)
			codec := "aac"
			if isOpusCodec(audio.Codec) {
				codec = "opus"
			} else {
				require.True(tb, isAACCodec(audio.Codec))
			}
			codecs[id], timescales[id] = codec, audio.Timescale()
		}
		for _, event := range events {
			if event.Type != "segment" && event.Type != "signed-segment" {
				continue
			}
			for id, duration := range event.Durations {
				require.NotZero(tb, timescales[id])
				seconds[codecs[id]] += float64(duration) / float64(timescales[id])
				samples[codecs[id]] += int(event.SampleCounts[id])
			}
		}
	}
	require.Equal(tb, 300, samples["video"], "video passthrough must retain every frame")
	for codec, duration := range seconds {
		require.InDelta(tb, 10, duration, 0.15, "%s total duration", codec)
		require.Positive(tb, samples[codec])
	}
	if dual {
		require.Contains(tb, seconds, "aac")
		require.Contains(tb, seconds, "opus")
	}
	tb.Logf("verified segments=%d samples=%v media_seconds=%v", len(segments), samples, seconds)
}

func forkBenchmarkComplete(ctx context.Context, signer *MediaSignerLocal, segments [][]byte, target string) ([][]byte, error) {
	key, err := signers.MarshalES256KPrivateKeyPEM(signer.Signer)
	if err != nil {
		return nil, err
	}
	manager := &MediaManager{cli: &config.CLI{BroadcasterHost: "benchmark.example"}}
	var completed [][]byte
	transcoder := manager.newStreamTranscoder(ctx, target, signer.Cert, key, func(_ any, segment []byte) {
		completed = append(completed, segment)
	})
	for i, segment := range segments {
		if err := transcoder.Feed(segment, i); err != nil {
			_ = transcoder.Close()
			return nil, err
		}
	}
	err = transcoder.Close()
	return completed, err
}

// Each operation processes the whole ten-second stream, including EOF drain.
// STREAMPLACE_BENCH_CASE selects the same operation for fresh-process checks.
func forkBenchmarkOperation(tb testing.TB, name, codec string) (func() ([][]byte, error), func([][]byte)) {
	tb.Helper()
	switch name {
	case "sign":
		signer := forkBenchmarkSigner(tb, "source")
		input := forkBenchmarkRead(tb, codec+".mp4")
		return func() ([][]byte, error) {
			return forkBenchmarkSign(context.Background(), signer, input)
		}, func(output [][]byte) { forkBenchmarkCheck(tb, output, nil, false) }
	case "validate":
		source := forkBenchmarkSegments(tb, codec)
		return func() ([][]byte, error) {
			for _, segment := range source {
				if _, err := ValidateMP4Media(context.Background(), segment); err != nil {
					return nil, err
				}
			}
			return source, nil
		}, func(output [][]byte) { forkBenchmarkCheck(tb, output, nil, false) }
	case "complete":
		source := forkBenchmarkSegments(tb, codec)
		signer := forkBenchmarkSigner(tb, "node")
		target := "aac"
		if codec == "aac" {
			target = "opus"
		}
		return func() ([][]byte, error) {
			return forkBenchmarkComplete(context.Background(), signer, source, target)
		}, func(output [][]byte) { forkBenchmarkCheck(tb, output, source, true) }
	case "worker":
		require.Equal(tb, "aac", codec, "the MP4 ingest worker accepts AAC input")
		source := forkBenchmarkSigner(tb, "source")
		node := forkBenchmarkSigner(tb, "node")
		sourceKey, err := signers.MarshalES256KPrivateKeyPEM(source.Signer)
		require.NoError(tb, err)
		nodeKey, err := signers.MarshalES256KPrivateKeyPEM(node.Signer)
		require.NoError(tb, err)
		cfg := IngestWorkerConfig{
			StreamerDID: source.Streamer(), KeyPEM: sourceKey, CertPEM: source.Cert,
			Manifest: source.PrebuiltManifest, NodeKeyPEM: nodeKey, NodeCertPEM: node.Cert,
			BroadcasterHost: "benchmark.example",
		}
		input := forkBenchmarkRead(tb, "aac.mp4")
		var early [][]byte
		return func() ([][]byte, error) {
				var wire bytes.Buffer
				early = nil
				if err := RunMP4IngestWorker(context.Background(), cfg, bytes.NewReader(input), ingestframe.NewWriter(&wire), func() []byte { return cfg.Manifest }); err != nil {
					return nil, err
				}
				reader := ingestframe.NewReader(&wire)
				var completed [][]byte
				for {
					typ, payload, err := reader.ReadFrame()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						return nil, fmt.Errorf("read worker frame: %w", err)
					}
					switch typ.String() {
					case "segment", "completed-source-segment":
						completed = append(completed, payload)
					case "source-segment":
						early = append(early, payload)
					case "error":
						return nil, fmt.Errorf("worker error: %s", payload)
					default:
						return nil, fmt.Errorf("unexpected worker frame %s", typ)
					}
				}
				return completed, nil
			}, func(output [][]byte) {
				forkBenchmarkCheck(tb, output, nil, true)
				if early != nil {
					require.Len(tb, early, len(output), "every early source receives a completion")
					for i := range output {
						require.True(tb, bytes.HasPrefix(output[i], early[i]), "completion preserves its early signed source")
					}
				}
			}
	default:
		tb.Fatalf("unknown benchmark operation %q", name)
		return nil, nil
	}
}

func TestForkUpstreamBenchmarkSmoke(t *testing.T) {
	forkBenchmarkDir(t)
	gstinit.InitGST()
	for _, name := range []string{"sign", "validate", "complete", "worker"} {
		for _, codec := range []string{"aac", "opus"} {
			if name == "worker" && codec != "aac" {
				continue
			}
			t.Run(name+"/"+codec, func(t *testing.T) {
				run, check := forkBenchmarkOperation(t, name, codec)
				output, err := run()
				require.NoError(t, err)
				check(output)
				if dir := os.Getenv("STREAMPLACE_BENCH_OUTPUT_DIR"); dir != "" {
					dir = filepath.Join(dir, name, codec)
					require.NoError(t, os.MkdirAll(dir, 0o700))
					canonical := bytes.Join(output, nil)
					require.NoError(t, os.WriteFile(filepath.Join(dir, "combinedcanonical.m4s"), canonical, 0o600))
					var flat bytes.Buffer
					require.NoError(t, muxl.RunMuxlWrap(t.Context(), bytes.NewReader(canonical), "flat", &flat))
					require.NoError(t, os.WriteFile(filepath.Join(dir, "flat.mp4"), flat.Bytes(), 0o600))
				}
			})
		}
	}
}

func TestForkUpstreamBenchmarkCold(t *testing.T) {
	name, codec := os.Getenv("STREAMPLACE_BENCH_CASE"), os.Getenv("STREAMPLACE_BENCH_CODEC")
	if name == "" {
		t.Skip("select one fresh-process operation with STREAMPLACE_BENCH_CASE and STREAMPLACE_BENCH_CODEC")
	}
	require.Contains(t, []string{"aac", "opus"}, codec)
	run, check := forkBenchmarkOperation(t, name, codec)
	var before, after syscall.Rusage
	require.NoError(t, syscall.Getrusage(syscall.RUSAGE_SELF, &before))
	start := time.Now()
	// TestMain initializes Gst; this includes the first operation and lazy
	// WASM initialization. External process timing also includes Gst startup.
	gstinit.InitGST()
	output, err := run()
	elapsed := time.Since(start)
	require.NoError(t, syscall.Getrusage(syscall.RUSAGE_SELF, &after))
	require.NoError(t, err)
	check(output)
	t.Logf("operation=%s codec=%s wall_seconds=%.9f user_cpu_seconds=%.9f system_cpu_seconds=%.9f media_seconds=10", name, codec, elapsed.Seconds(),
		float64(after.Utime.Nano()-before.Utime.Nano())/1e9, float64(after.Stime.Nano()-before.Stime.Nano())/1e9)
}

func BenchmarkForkUpstream(b *testing.B) {
	forkBenchmarkDir(b)
	gstinit.InitGST()
	for _, name := range []string{"sign", "validate", "complete", "worker"} {
		for _, codec := range []string{"aac", "opus"} {
			if name == "worker" && codec != "aac" {
				continue
			}
			b.Run(name+"/"+codec, func(b *testing.B) {
				run, check := forkBenchmarkOperation(b, name, codec)
				output, err := run()
				require.NoError(b, err)
				check(output)
				b.ReportAllocs()
				var before, after syscall.Rusage
				b.ResetTimer()
				require.NoError(b, syscall.Getrusage(syscall.RUSAGE_SELF, &before))
				for i := 0; i < b.N; i++ {
					output, err = run()
					if err != nil {
						b.Fatal(err)
					}
				}
				require.NoError(b, syscall.Getrusage(syscall.RUSAGE_SELF, &after))
				b.StopTimer()
				b.ReportMetric(10, "media_seconds/op")
				b.ReportMetric(float64(after.Utime.Nano()-before.Utime.Nano())/1e9/float64(b.N), "user_cpu_seconds/op")
				b.ReportMetric(float64(after.Stime.Nano()-before.Stime.Nano())/1e9/float64(b.N), "system_cpu_seconds/op")
				check(output)
			})
		}
	}
}
