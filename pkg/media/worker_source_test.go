package media

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/crypto/signers"
	"stream.place/streamplace/pkg/ingestframe"
	"stream.place/streamplace/pkg/livehls"
)

func workerSourceForTest(t *testing.T) (string, []byte) {
	t.Helper()
	ms := newBareSegmentSigner(t)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(ms.PrebuiltManifest, &manifest))
	assertions := manifest["assertions"].([]any)
	metadata := assertions[1].(map[string]any)["data"].(map[string]any)
	metadata["dc:creator"] = ms.did
	metadata["@context"].(map[string]any)["Iptc4xmpExt"] = "http://iptc.org/std/Iptc4xmpExt/2008-02-29/"
	metadata["Iptc4xmpExt:ContentWarning"] = []any{"flashing lights"}
	assertions[0].(map[string]any)["data"].(map[string]any)["actions"] = []any{
		map[string]any{"action": "c2pa.created"},
		map[string]any{"action": "c2pa.published"},
	}
	manifest["assertions"] = append(assertions, map[string]any{
		"label": "place.stream.metadata.configuration",
		"data": map[string]any{"distributionPolicy": map[string]any{
			"allowedBroadcasters": []any{"did:web:node.test"},
		}},
	})
	var err error
	ms.PrebuiltManifest, err = json.Marshal(manifest)
	require.NoError(t, err)
	segments := allSignedBareSegments(t, context.Background(), ms, getFixture("h264-opus-frag.mp4"))
	require.NotEmpty(t, segments)
	return ms.did, segments[0]
}

// Source publication must carry the original signed bytes and playable samples,
// without starting canonical archival, HLS, or director side effects.
func TestWorkerSourceFramePublishesBeforeCompletion(t *testing.T) {
	ctx := context.Background()
	streamer, source := workerSourceForTest(t)
	mm := &MediaManager{
		cli:         &config.CLI{BroadcasterHost: "node.test", AllowedStreams: []string{streamer}},
		bus:         bus.NewBus(),
		liveWindows: map[string]*livehls.Writer{},
	}
	playback := mm.bus.SubscribeSegment(ctx, streamer, PlaybackSourceRendition)
	defer mm.bus.UnsubscribeSegment(ctx, streamer, PlaybackSourceRendition, playback)
	var frames bytes.Buffer
	w := ingestframe.NewWriter(&frames)
	require.NoError(t, w.SourceSegment(source))
	require.NoError(t, w.End())
	legacyCalls := 0
	progress := 0
	sawEnd, err := mm.consumeWorkerFrames(ctx, ingestframe.NewReader(&frames), streamer, func([]byte) error {
		legacyCalls++
		return nil
	}, func() { progress++ })
	require.NoError(t, err)
	require.True(t, sawEnd)
	require.Zero(t, legacyCalls)
	require.Equal(t, 1, progress, "source frames must not hide stalled codec completion; only End counts here")
	require.Len(t, playback.C, 1, "verified source is playable before a completed frame arrives")
	got := <-playback.C
	require.Equal(t, source, got.Muxl, "source signatures and canonical bytes survive publication")
	valid, err := ValidateMP4MediaC2PA(ctx, source)
	require.NoError(t, err)
	require.Equal(t, *valid.Manifest.Label, got.Filepath)
	require.True(t, got.Published)
	require.NotNil(t, got.PacketizedData)
	require.NotEmpty(t, got.PacketizedData.Video)
	require.NotEmpty(t, got.PacketizedData.Audio)
	require.Empty(t, mm.BufferedSegments(streamer), "canonical distribution waits for completion")
	require.Nil(t, mm.GetLiveWindow(streamer))
	require.False(t, mm.bus.HasSegments(streamer, PlaybackSourceRendition), "source-only end cannot leave a live playback cache")
}

// A phase frame is subject to the same signature, identity and distribution
// checks as an ordinary ingest segment. The bitrate gate waits for completion.
func TestWorkerSourceFrameAdmission(t *testing.T) {
	previousVerbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "3"))
	t.Cleanup(func() { require.NoError(t, flag.Set("v", previousVerbosity)) })
	streamer, source := workerSourceForTest(t)
	tests := []struct {
		name   string
		change func(*config.CLI, *string, *[]byte)
		error  string
	}{
		{name: "bitrate guard", change: func(cli *config.CLI, _ *string, _ *[]byte) { cli.MaximumLiveBitrate = 8_000_000 }},
		{name: "named streamer", change: func(_ *config.CLI, named *string, _ *[]byte) { *named = "did:key:different" }, error: "not did:key:different"},
		{name: "missing streamer", change: func(_ *config.CLI, named *string, _ *[]byte) { *named = "" }, error: "no named streamer"},
		{name: "signature", change: func(_ *config.CLI, _ *string, segment *[]byte) { (*segment)[len(*segment)-1] ^= 1 }, error: "failed to validate MP4 media"},
		{name: "allow list", change: func(cli *config.CLI, _ *string, _ *[]byte) { cli.AllowedStreams = []string{"did:key:different"} }, error: "not allowed"},
		{name: "distribution", change: func(cli *config.CLI, _ *string, _ *[]byte) { cli.BroadcasterHost = "other.test" }, error: "not allowed to distribute"},
		{name: "content warning", change: func(cli *config.CLI, _ *string, _ *[]byte) {
			cli.ContentFilters = &config.ContentFilters{}
			cli.ContentFilters.ContentWarnings.Enabled = true
			cli.ContentFilters.ContentWarnings.BlockedWarnings = []string{"flashing lights"}
		}, error: "content warning blocked"},
	}
	for _, phase := range []ingestframe.Type{ingestframe.SourceSegment, ingestframe.CompletedSourceSegment} {
		for _, tc := range tests {
			if phase == ingestframe.CompletedSourceSegment && tc.error == "" {
				continue // successful bitrate-guarded completion is tested separately
			}
			t.Run(phase.String()+"/"+tc.name, func(t *testing.T) {
				var logs logCapture
				previous := slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
				defer slog.SetDefault(previous)
				cli := &config.CLI{BroadcasterHost: "node.test", AllowedStreams: []string{streamer}}
				named, segment := streamer, bytes.Clone(source)
				tc.change(cli, &named, &segment)
				mm := &MediaManager{cli: cli, bus: bus.NewBus()}
				playback := mm.bus.SubscribeSegment(context.Background(), named, PlaybackSourceRendition)
				defer mm.bus.UnsubscribeSegment(context.Background(), named, PlaybackSourceRendition, playback)
				var frames bytes.Buffer
				require.NoError(t, ingestframe.NewWriter(&frames).WriteFrame(phase, segment))
				_, err := mm.consumeWorkerFrames(context.Background(), ingestframe.NewReader(&frames), named, nil, nil)
				require.NoError(t, err, "one rejected GoP does not stop the stream")
				require.Empty(t, playback.C)
				require.Empty(t, mm.BufferedSegments(streamer))
				if tc.error != "" {
					require.Contains(t, logs.String(), tc.error)
				} else {
					require.NotContains(t, logs.String(), "level=ERROR")
				}
			})
		}
	}
}

// Completion is independently validated and reaches canonical consumers even
// when main did not receive its source frame during a restart.
func TestWorkerCompletedSourceFrameDistributes(t *testing.T) {
	ctx := context.Background()
	streamer, source := workerSourceForTest(t)
	for _, bitrateGuard := range []bool{false, true} {
		name := "early playback"
		if bitrateGuard {
			name = "bitrate guard"
		}
		t.Run(name, func(t *testing.T) {
			mm := &MediaManager{
				cli:         &config.CLI{BroadcasterHost: "node.test", AllowedStreams: []string{streamer}},
				bus:         bus.NewBus(),
				liveWindows: map[string]*livehls.Writer{},
			}
			if bitrateGuard {
				mm.cli.MaximumLiveBitrate = 8_000_000
			}
			notifications := mm.NewSegment()
			t.Cleanup(func() { close(mm.newSegmentSubs[0].queue) })
			var frames bytes.Buffer
			require.NoError(t, ingestframe.NewWriter(&frames).CompletedSourceSegment(source))
			_, err := mm.consumeWorkerFrames(ctx, ingestframe.NewReader(&frames), streamer, nil, nil)
			require.NoError(t, err)
			buffered := mm.BufferedSegments(streamer)
			require.Len(t, buffered, 1, "completion reaches the canonical segment buffer")
			require.Equal(t, source, buffered[0].Data)
			select {
			case got := <-notifications:
				require.Equal(t, source, got.Muxl)
				require.Equal(t, streamer, got.Segment.RepoDID)
				require.NotEmpty(t, got.Data)
				require.True(t, got.Metadata.Published)
				require.Equal(t, !bitrateGuard, got.PlaybackPublished, "bitrate-guarded completion must still own playback")
			case <-time.After(time.Second):
				t.Fatal("completion did not notify canonical consumers")
			}
		})
	}
}

func TestWorkerEmitsSourceBeforeCodecCompletion(t *testing.T) {
	ctx := context.Background()
	_, source := workerSourceForTest(t)
	ms := newBareSegmentSigner(t)
	key, err := signers.MarshalES256KPrivateKeyPEM(ms.Signer)
	require.NoError(t, err)
	reader, writer := io.Pipe()
	type frame struct {
		typ     ingestframe.Type
		payload []byte
	}
	output := make(chan frame, 16)
	done := make(chan error, 1)
	go func() {
		defer close(output)
		r := ingestframe.NewReader(reader)
		for {
			typ, payload, err := r.ReadFrame()
			if err != nil {
				done <- err
				return
			}
			output <- frame{typ, payload}
		}
	}()
	mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "node.test"}}
	onSegment, flush := mm.workerSegmentSink(ctx, IngestWorkerConfig{
		NodeCertPEM: ms.Cert, NodeKeyPEM: key,
	}, ingestframe.NewWriter(writer))
	defer func() {
		flush()
		require.NoError(t, writer.Close())
		for range output {
		}
		require.ErrorIs(t, <-done, io.EOF)
	}()
	require.NoError(t, onSegment(ctx, source))
	select {
	case first := <-output:
		require.Equal(t, ingestframe.SourceSegment, first.typ)
		require.Equal(t, source, first.payload)
	case <-time.After(time.Second):
		t.Fatal("source must be framed before the continuous codec completion is flushed")
	}
}

func TestWorkerWatchdogRequiresCodecCompletion(t *testing.T) {
	previousVerbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "3"))
	t.Cleanup(func() { require.NoError(t, flag.Set("v", previousVerbosity)) })
	for _, sourceOnly := range []bool{true, false} {
		name := "completion resets deadline"
		if sourceOnly {
			name = "source cannot hide stalled completion"
		}
		t.Run(name, func(t *testing.T) {
			var logs logCapture
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(previous)
			synctest.Test(t, func(t *testing.T) {
				wedged := make(chan struct{})
				wd := newWorkerWatchdog(context.Background(), time.Minute, func() { close(wedged) }, "did:test:worker")
				defer wd.stop()
				frames := wd.wrap(ingestframe.NewWriter(io.Discard))
				time.Sleep(45 * time.Second)
				if sourceOnly {
					require.NoError(t, frames.SourceSegment([]byte("source")))
				} else {
					require.NoError(t, frames.CompletedSourceSegment([]byte("completion")))
				}
				time.Sleep(15 * time.Second)
				synctest.Wait()
				if !sourceOnly {
					select {
					case <-wedged:
						t.Fatal("completed media must extend the worker's deadline")
					default:
					}
					time.Sleep(45 * time.Second)
					synctest.Wait()
				}
				select {
				case <-wedged:
				default:
					t.Fatal("worker must stop when codec completion stalls")
				}
			})
			require.Contains(t, logs.String(), "ingest worker watchdog fired")
		})
	}
}

func TestWorkerSourceOnlyTeardownPreservesNewerPlayback(t *testing.T) {
	ctx := context.Background()
	streamer, source := workerSourceForTest(t)
	mm := &MediaManager{
		cli: &config.CLI{BroadcasterHost: "node.test", AllowedStreams: []string{streamer}},
		bus: bus.NewBus(),
	}
	playback := mm.bus.SubscribeSegment(ctx, streamer, PlaybackSourceRendition)
	defer mm.bus.UnsubscribeSegment(ctx, streamer, PlaybackSourceRendition, playback)
	consume := func() (*io.PipeWriter, <-chan error) {
		reader, writer := io.Pipe()
		done := make(chan error, 1)
		go func() {
			_, err := mm.consumeWorkerFrames(ctx, ingestframe.NewReader(reader), streamer, nil, nil)
			done <- err
		}()
		t.Cleanup(func() { writer.Close(); reader.Close() })
		require.NoError(t, ingestframe.NewWriter(writer).SourceSegment(source))
		select {
		case <-playback.C:
		case <-time.After(time.Second):
			t.Fatal("source frame was not published")
		}
		return writer, done
	}
	older, olderDone := consume()
	newer, newerDone := consume()
	_, err := older.Write([]byte{0xa1}) // a worker dies inside its next CBOR item
	require.NoError(t, err)
	require.NoError(t, older.Close())
	require.ErrorIs(t, <-olderDone, io.ErrUnexpectedEOF)
	require.True(t, mm.bus.HasSegments(streamer, PlaybackSourceRendition), "older worker teardown cannot clear a newer worker's source")
	require.NoError(t, newer.Close())
	require.NoError(t, <-newerDone)
	require.False(t, mm.bus.HasSegments(streamer, PlaybackSourceRendition), "last source-only worker must release its live cache")
}
