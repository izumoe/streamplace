package websocketrep

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	official "moq.dev/moq"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/moq"
	"stream.place/streamplace/pkg/muxl"
)

func TestMoqReplicationNegotiatesFullSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	segments := signedSegments(t, ctx)
	const streamer = "did:plc:compat"
	for _, tc := range []struct {
		name         string
		code         uint16
		wantFallback bool
		wantError    bool
		stall        bool
		stallLegacy  bool
	}{
		{name: "full peer"},
		{name: "old peer", code: 404, wantFallback: true},
		{name: "forbidden", code: 403, wantError: true},
		{name: "server failure", code: 500, wantError: true},
		{name: "stalled catalog", wantError: true, stall: true},
		{name: "stalled legacy catalog", code: 404, wantFallback: true, stallLegacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			origin := official.NewOriginProducer()
			requests, err := origin.Dynamic("", official.Route{})
			require.NoError(t, err)
			defer requests.Cancel()
			full, err := official.NewBroadcastProducer()
			require.NoError(t, err)
			defer full.Close()
			legacy, err := official.NewBroadcastProducer()
			require.NoError(t, err)
			defer legacy.Close()
			for i, producer := range []*official.BroadcastProducer{legacy, full} {
				if tc.stall && i == 1 || tc.stallLegacy && i == 0 {
					continue
				}
				tracks := moqTracks{}
				defer tracks.finish()
				require.NoError(t, tracks.publish(ctx, producer, segments[i], false, func(track *official.CMAFProducer) (*official.CMAFGroupProducer, error) {
					return track.CreateGroup(0)
				}))
			}
			paths := make(chan string, 4)
			go func() {
				for req, err := range requests.Requests(ctx) {
					if err != nil {
						return
					}
					path, err := req.Path()
					if err != nil {
						return
					}
					paths <- path
					switch path {
					case "source/" + streamer:
						if tc.code != 0 {
							_ = req.Reject(tc.code)
						} else {
							_ = req.Accept(full)
						}
					case streamer:
						_ = req.Accept(legacy)
					default:
						_ = req.Reject(404)
					}
				}
			}()
			client := newMoqTestClient(t, ctx, origin)
			requestCtx := ctx
			if tc.stall || tc.stallLegacy {
				var stop context.CancelFunc
				requestCtx, stop = context.WithTimeout(ctx, 100*time.Millisecond)
				defer stop()
			}
			broadcast, err := requestReplicationBroadcast(requestCtx, client, streamer)
			if tc.stall || tc.stallLegacy {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			} else if tc.wantError {
				require.Error(t, err)
				protocol, ok := official.ProtocolError(err)
				require.True(t, ok)
				require.Equal(t, uint32(tc.code)+64, protocol.Code)
			} else {
				require.NoError(t, err)
				finished := errors.New("received source")
				var received []byte
				err := consumeBroadcast(ctx, broadcast, func(segment []byte) error { received = segment; return finished }, func(context.Context, []byte) error { return nil })
				require.ErrorIs(t, err, finished)
				want := segments[1]
				if tc.wantFallback {
					want = segments[0]
				}
				require.Equal(t, want, received, "negotiation preserves the chosen source's signed bytes")
			}
			require.NoError(t, ctx.Err())
			require.Equal(t, "source/"+streamer, <-paths)
			if tc.wantFallback {
				require.Equal(t, streamer, <-paths)
			}
			select {
			case path := <-paths:
				t.Fatalf("unexpected additional request %q", path)
			default:
			}
		})
	}
}

func TestMoqSourceRoutesPreserveFullLadder(t *testing.T) {
	require.NoError(t, muxl.Initialize())
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	fixture, err := os.ReadFile("../../../test/fixtures/h264-opus-frag.mp4")
	require.NoError(t, err)
	canonical, err := muxl.RunMuxlCanonicalize(ctx, fixture, map[uint32]uint32{1: 1, 2: 3})
	require.NoError(t, err)
	base, err := media.MuxlTracks(ctx, canonical)
	require.NoError(t, err)
	relabelled, err := muxl.RunMuxlCanonicalize(ctx, fixture, map[uint32]uint32{1: 65538, 2: 3})
	require.NoError(t, err)
	extra, err := media.MuxlTracks(ctx, relabelled)
	require.NoError(t, err)
	require.NotEmpty(t, base["video1"])
	require.NotEmpty(t, base["audio3"])
	require.NotEmpty(t, extra["video65538"])
	var flat bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(bytes.Join([][]byte{base["video1"], extra["video65538"], base["audio3"]}, nil)), "fmp4", &flat))
	signer, err := media.NewEphemeralMediaSigner("compat-routing")
	require.NoError(t, err)
	events := make(chan *muxl.MuxlEvent, 16)
	signed := make(chan error, 1)
	go func() {
		signed <- signer.SignSegmentStream(ctx, bytes.NewReader(flat.Bytes()), events, nil)
		close(events)
	}()
	var source []byte
	for event := range events {
		if event.Type != "signed-segment" {
			continue
		}
		for _, id := range []string{"1", "65538", "3"} {
			source = append(source, event.Tracks[id]...)
		}
	}
	require.NoError(t, <-signed)
	require.NotEmpty(t, source)
	tracks, err := media.MuxlTracks(ctx, source)
	require.NoError(t, err)
	var sourceNames []string
	for name := range tracks {
		sourceNames = append(sourceNames, name)
	}
	require.ElementsMatch(t, []string{"audio3", "video1", "video65538"}, sourceNames, "the fixture carries both signed video assets")
	source = bytes.Join([][]byte{tracks["video1"], tracks["video65538"], tracks["audio3"]}, nil)
	legacy := bytes.Join([][]byte{tracks["video1"], tracks["audio3"]}, nil)
	b := bus.NewBus()
	const streamer = "did:plc:routing"
	for _, channel := range []string{moqSourceChannel, media.PlaybackSourceRendition} {
		b.PublishSegment(ctx, streamer, channel, &bus.Seg{Muxl: []byte("private source"), Published: false})
		b.PublishSegment(ctx, streamer, channel, &bus.Seg{Muxl: source, Published: true})
	}
	r := &WebsocketReplicator{bus: b, cli: &config.CLI{}}
	origin := official.NewOriginProducer()
	requests, err := origin.Dynamic("", official.Route{})
	require.NoError(t, err)
	requested := make(chan struct{})
	go func() {
		defer close(requested)
		var wg sync.WaitGroup
		defer wg.Wait()
		for req, err := range requests.Requests(ctx) {
			if err != nil {
				return
			}
			wg.Add(1)
			go func() { defer wg.Done(); r.serveBroadcast(ctx, req) }()
		}
	}()
	defer func() { cancel(); requests.Cancel(); <-requested }()
	client := newMoqTestClient(t, ctx, origin)
	ended := make([]<-chan error, 0, 2)
	for _, tc := range []struct {
		path       string
		want       []byte
		addendum   []byte
		videoCount int
	}{
		{streamer, legacy, tracks["video65538"], 2},
		{"source/" + streamer, legacy, tracks["video65538"], 2},
	} {
		broadcast, err := requestMoqBroadcast(ctx, client, tc.path)
		require.NoError(t, err)
		catalog, err := broadcast.Catalog(ctx)
		require.NoError(t, err)
		require.Len(t, catalog.Video, tc.videoCount, tc.path)
		received := make(chan []byte, 1)
		addenda := make(chan []byte, 1)
		done := make(chan error, 1)
		go func() {
			done <- consumeBroadcast(ctx, broadcast, func(segment []byte) error { received <- segment; return nil }, func(_ context.Context, data []byte) error { addenda <- data; return nil })
		}()
		select {
		case got := <-received:
			require.Equal(t, tc.want, got, "only published source assets reach %s, without re-signing", tc.path)
		case err := <-done:
			t.Fatalf("%s ended before its source arrived: %v", tc.path, err)
		case <-ctx.Done():
			t.Fatal("source did not arrive")
		}
		if tc.addendum != nil {
			select {
			case got := <-addenda:
				require.Equal(t, tc.addendum, got, "legacy rendition transport preserves the original signed asset")
			case err := <-done:
				t.Fatalf("rendition ended before arrival: %v", err)
			case <-ctx.Done():
				t.Fatal("legacy rendition did not arrive")
			}
		}
		ended = append(ended, done)
	}
	playback, err := requestMoqBroadcast(ctx, client, "playback/"+streamer)
	require.NoError(t, err)
	catalog, err := playback.Catalog(ctx)
	require.NoError(t, err)
	require.Len(t, catalog.Video, 2, "browser playback keeps the source ladder")
	b.EndSession(ctx, streamer, moqSourceChannel)
	for _, done := range ended {
		select {
		case err := <-done:
			require.Error(t, err)
			require.NotErrorIs(t, err, context.DeadlineExceeded, "both replication routes end with the source session")
		case <-ctx.Done():
			t.Fatal("replication outlived its source session")
		}
	}
}

func TestMoqLegacyRejectsAV1OnlySource(t *testing.T) {
	require.NoError(t, muxl.Initialize())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	fixture, err := os.ReadFile("../../../test/fixtures/av1-aac-frag.mp4")
	require.NoError(t, err)
	signer, err := media.NewEphemeralMediaSigner("av1-only-compat")
	require.NoError(t, err)
	events := make(chan *muxl.MuxlEvent, 16)
	signed := make(chan error, 1)
	go func() { signed <- signer.SignSegmentStream(ctx, bytes.NewReader(fixture), events, nil); close(events) }()
	var source []byte
	for event := range events {
		if event.Type == "signed-segment" {
			source = append(source, event.Tracks["1"]...)
			source = append(source, event.Tracks["2"]...)
		}
	}
	require.NoError(t, <-signed)
	require.NotEmpty(t, source)
	b := bus.NewBus()
	const streamer = "did:plc:av1-only"
	for _, channel := range []string{moqSourceChannel, media.PlaybackSourceRendition} {
		b.PublishSegment(ctx, streamer, channel, &bus.Seg{Muxl: source, Published: true})
	}
	r := &WebsocketReplicator{bus: b, cli: &config.CLI{}}
	origin := official.NewOriginProducer()
	requests, err := origin.Dynamic("", official.Route{})
	require.NoError(t, err)
	served := make(chan struct{})
	go func() {
		defer close(served)
		var wg sync.WaitGroup
		defer wg.Wait()
		for req, err := range requests.Requests(ctx) {
			if err != nil {
				return
			}
			wg.Add(1)
			go func() { defer wg.Done(); r.serveBroadcast(ctx, req) }()
		}
	}()
	defer func() { cancel(); requests.Cancel(); <-served }()
	client := newMoqTestClient(t, ctx, origin)
	legacyCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	legacy, err := requestMoqBroadcast(legacyCtx, client, streamer)
	if err == nil {
		_, err = legacy.Catalog(legacyCtx)
	}
	require.Error(t, err)
	require.NotErrorIs(t, err, context.DeadlineExceeded, "an incompatible source must fail explicitly instead of stranding the peer")
	protocol, ok := official.ProtocolError(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, official.ErrorScopeStream, protocol.Scope)
	require.Equal(t, official.ProtocolKindApp, protocol.Kind)
	require.Equal(t, uint32(500+64), protocol.Code, "an existing incompatible source is not a missing source")
	require.NoError(t, ctx.Err())
	for _, path := range []string{"source/" + streamer, "playback/" + streamer} {
		broadcast, err := requestMoqBroadcast(ctx, client, path)
		require.NoError(t, err)
		catalog, err := broadcast.Catalog(ctx)
		require.NoError(t, err)
		require.Len(t, catalog.Video, 1, path)
		if path == "playback/"+streamer {
			continue
		}
		finished := errors.New("received AV1 source")
		var received []byte
		err = consumeBroadcast(ctx, broadcast, func(data []byte) error { received = data; return finished }, func(context.Context, []byte) error { return nil })
		require.ErrorIs(t, err, finished)
		wantTracks, err := media.MuxlTracks(ctx, source)
		require.NoError(t, err)
		gotTracks, err := media.MuxlTracks(ctx, received)
		require.NoError(t, err)
		require.Equal(t, wantTracks, gotTracks, path)
	}
}

func TestMoqLegacySourceDoesNotSwitchVideoAfterPartialEOF(t *testing.T) {
	require.NoError(t, muxl.Initialize())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	fixture, err := os.ReadFile("../../../test/fixtures/h264-opus-frag.mp4")
	require.NoError(t, err)
	canonical, err := muxl.RunMuxlCanonicalize(ctx, fixture, map[uint32]uint32{1: 65538, 2: 3})
	require.NoError(t, err)
	signer, err := media.NewEphemeralMediaSigner("legacy-partial-eof")
	require.NoError(t, err)
	sign := func(canonical []byte) [][]byte {
		t.Helper()
		var input bytes.Buffer
		require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(canonical), "fmp4", &input))
		events := make(chan *muxl.MuxlEvent, 16)
		signed := make(chan error, 1)
		go func() {
			signed <- signer.SignSegmentStream(ctx, bytes.NewReader(input.Bytes()), events, nil)
			close(events)
		}()
		var segments [][]byte
		for event := range events {
			if event.Type == "signed-segment" {
				segments = append(segments, bytes.Join([][]byte{event.Tracks["1"], event.Tracks["65538"], event.Tracks["3"]}, nil))
			}
		}
		require.NoError(t, <-signed)
		require.GreaterOrEqual(t, len(segments), 2)
		return segments
	}
	other := sign(canonical)
	lowerCanonical, err := muxl.RunMuxlCanonicalize(ctx, fixture, map[uint32]uint32{1: 1, 2: 3})
	require.NoError(t, err)
	lower := sign(lowerCanonical)
	otherTracks, err := media.MuxlTracks(ctx, other[0])
	require.NoError(t, err)
	first := append(bytes.Clone(lower[0]), otherTracks["video65538"]...)
	tail := other[1]
	tailTracks, err := media.MuxlTracks(ctx, tail)
	require.NoError(t, err)
	require.Empty(t, tailTracks["video1"])
	require.NotEmpty(t, tailTracks["video65538"])
	require.NotEmpty(t, tailTracks["audio3"])
	broadcast, err := official.NewBroadcastProducer()
	require.NoError(t, err)
	defer broadcast.Close()
	consumer, err := broadcast.Consume()
	require.NoError(t, err)
	tracks := moqTracks{}
	defer tracks.finish()
	publish := func(data []byte, sequence uint64) error {
		return tracks.publish(ctx, broadcast, data, true, func(track *official.CMAFProducer) (*official.CMAFGroupProducer, error) {
			return track.CreateGroup(sequence)
		})
	}
	require.NoError(t, publish(first, 0))
	catalog, err := consumer.Catalog(ctx)
	require.NoError(t, err)
	require.Contains(t, catalog.Video, "video1")
	require.Len(t, catalog.Video, 2, "the second AVC remains available as an addendum")
	err = publish(tail, 1)
	require.Error(t, err, "a finished selected source cannot silently replace the legacy peer's video")
	catalog, err = consumer.Catalog(ctx)
	require.NoError(t, err)
	require.Contains(t, catalog.Video, "video1")
	require.Len(t, catalog.Video, 2, "a legacy session never switches its fixed source track")
}

func newMoqTestClient(t *testing.T, ctx context.Context, origin *official.OriginProducer) *official.Client {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	cert, err := moq.NewSelfSignedCert()
	require.NoError(t, err)
	tlsFiles, err := moq.ManagedTLSFiles(t.TempDir(), cert.Certificate)
	require.NoError(t, err)
	require.NoError(t, tlsFiles.Sync())
	server, err := official.Listen(ctx, "127.0.0.1:0", official.WithTLSCert(tlsFiles.CertPath), official.WithTLSKey(tlsFiles.KeyPath), official.WithServerPublishOrigin(origin))
	require.NoError(t, err)
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		server.Close()
		if err := <-served; err != nil {
			require.ErrorIs(t, err, context.Canceled)
		}
	})
	r := &WebsocketReplicator{}
	client, err := r.dialMoq(ctx, fmt.Sprintf("moqt://%s?certhash=%s", server.LocalAddr(), cert.Hash()))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}
