package websocketrep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	official "moq.dev/moq"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/gstinit"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/moq"
	"stream.place/streamplace/pkg/muxl"
)

// signedSegments signs the fragmented fixture the way live ingest does and
// returns each GoP's bare canonical segment (video1 + audio2).
func signedSegments(t *testing.T, ctx context.Context) [][]byte {
	t.Helper()
	ms, err := media.NewEphemeralMediaSigner("test-streamer")
	require.NoError(t, err)
	frag, err := os.ReadFile("../../../test/fixtures/h264-opus-frag.mp4")
	require.NoError(t, err)
	events := make(chan *muxl.MuxlEvent, 16)
	signed := make(chan error, 1)
	go func() {
		signed <- ms.SignSegmentStream(ctx, bytes.NewReader(frag), events)
		close(events)
	}()
	var segments [][]byte
	for ev := range events {
		if ev.Type != "signed-segment" {
			continue
		}
		ids := make([]int, 0, len(ev.Tracks))
		for id := range ev.Tracks {
			n, err := strconv.Atoi(id)
			require.NoError(t, err)
			ids = append(ids, n)
		}
		slices.Sort(ids)
		var segment []byte
		for _, id := range ids {
			segment = append(segment, ev.Tracks[strconv.Itoa(id)]...)
		}
		segments = append(segments, segment)
	}
	require.NoError(t, <-signed)
	require.GreaterOrEqual(t, len(segments), 2)
	return segments
}

// renditionOf relabels segment's video as rendition track 0, the shape of
// an addendum MintVideoRenditions publishes (unsigned: nothing here
// verifies it).
func renditionOf(t *testing.T, ctx context.Context, segment []byte) []byte {
	t.Helper()
	var fmp4 bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(segment), "fmp4", &fmp4))
	canon, err := muxl.RunMuxlCanonicalize(ctx, fmp4.Bytes(), map[uint32]uint32{1: media.RenditionTrackID(0)})
	require.NoError(t, err)
	tracks, err := media.MuxlTracks(ctx, canon)
	require.NoError(t, err)
	rendition := tracks[fmt.Sprintf("video%d", media.RenditionTrackID(0))]
	require.NotEmpty(t, rendition)
	return rendition
}

// A peer pulling a stream through the listener finds each MUXL track in
// the broadcast's catalog as a CMAF track a hang player can decode, and
// gets the bus's source segments back byte for byte, primed with the recent
// ones, in order, plus the renditions on their own, until the stream's
// session ends; a stream that is not live here is refused rather than
// waited on.
func TestMoqServesTheBus(t *testing.T) {
	gstinit.InitGST()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	interval := moqLivenessInterval
	moqLivenessInterval = 10 * time.Millisecond
	t.Cleanup(func() { moqLivenessInterval = interval })
	segments := signedSegments(t, ctx)
	rendition := renditionOf(t, ctx, segments[0])

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := pc.LocalAddr().String()
	require.NoError(t, pc.Close())
	cert, err := moq.NewSelfSignedCert()
	require.NoError(t, err)
	tlsFiles, err := moq.ManagedTLSFiles(t.TempDir(), cert.Certificate)
	require.NoError(t, err)
	b := bus.NewBus()
	// Listening the way --moq-addr defaults, with no host.
	_, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	r := &WebsocketReplicator{bus: b, cli: &config.CLI{MoqAddr: ":" + port}, MoqTLS: tlsFiles}
	served := make(chan error, 1)
	go func() { served <- r.serveMoQ(ctx) }()
	defer func() {
		cancel()
		<-served //nolint:errcheck // cancelled on purpose
	}()

	// Dialled the way a pull dials: the advertised URL, certificate pinned.
	moqURL := fmt.Sprintf("moqt://%s?%s=%s", addr, moq.CertHashParam, cert.Hash())
	var client *official.Client
	require.Eventually(t, func() bool {
		client, err = r.dialMoq(ctx, moqURL)
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "the listener comes up")
	defer client.Close()

	// The refusal reaches a puller by the time it subscribes.
	unexpected := func([]byte) error { return errors.New("a segment of a stream that is not live") }
	offline, err := requestMoqBroadcast(ctx, client, "did:plc:offline")
	if err == nil {
		err = consumeBroadcast(ctx, offline, unexpected, func(_ context.Context, b []byte) error { return unexpected(b) })
	}
	require.Error(t, err, "a stream that is not live is refused")
	require.NotContains(t, err.Error(), "a segment of", "a stream that is not live is refused")
	require.NoError(t, ctx.Err(), "refused, not waited on")

	const streamer = "did:plc:live"
	b.PublishSegment(ctx, streamer, moqSourceChannel, &bus.Seg{Muxl: segments[0], Published: false})
	b.PublishSegment(ctx, streamer, moqSourceChannel, &bus.Seg{Muxl: segments[1], Published: true})
	broadcast, err := requestMoqBroadcast(ctx, client, streamer)
	require.NoError(t, err)

	got := make(chan []byte, 8)
	renditions := make(chan []byte, 8)
	done := make(chan error, 1)
	go func() {
		done <- consumeBroadcast(ctx, broadcast,
			func(segment []byte) error { got <- segment; return nil },
			func(_ context.Context, addendum []byte) error { renditions <- addendum; return nil })
	}()
	next := func(ch chan []byte) []byte {
		select {
		case payload := <-ch:
			return payload
		case err := <-done:
			t.Fatalf("the broadcast ended early: %v", err)
		case <-ctx.Done():
			t.Fatal("no segment arrived")
		}
		return nil
	}
	require.Equal(t, segments[1], next(got), "primed with the bus's recent published segment, reassembled from its tracks")

	catalog, err := broadcast.Catalog(ctx)
	require.NoError(t, err)
	video, audio := catalog.Video["video1"], catalog.Audio["audio2"]
	for name, container := range map[string]official.Container{"video1": video.Container, "audio2": audio.Container} {
		cmaf, ok := container.(official.ContainerCmaf)
		require.True(t, ok, "%s is a CMAF track", name)
		require.NotEmpty(t, cmaf.Init, "%s carries its init", name)
	}
	require.NotNil(t, video.Description, "H.264 carries its avcC, which hang decoders need")

	b.PublishSegment(ctx, streamer, media.RenditionsChannel, &bus.Seg{Muxl: rendition, Published: true})
	require.Equal(t, rendition, next(renditions), "a rendition travels on its own track")

	// Browser playback can start from the verified source while canonical
	// codec completion is still pending on the peer replication channel.
	const earlyStreamer = "did:plc:early"
	packets := make([]*bus.PacketizedSegment, len(segments))
	shortest, longest := 0, 0
	for i, segment := range segments {
		packets[i], err = media.Packetize(ctx, &config.CLI{}, &bus.Seg{Muxl: segment})
		require.NoError(t, err)
		if packets[i].BatchDuration < packets[shortest].BatchDuration {
			shortest = i
		}
		if packets[i].BatchDuration > packets[longest].BatchDuration {
			longest = i
		}
	}
	require.Less(t, packets[shortest].BatchDuration, packets[longest].BatchDuration, "fixture includes a shorter final batch")
	publishEarly := func(i int) {
		b.PublishSegment(ctx, earlyStreamer, media.PlaybackSourceRendition, &bus.Seg{Muxl: segments[i], PacketizedData: packets[i], Published: true})
	}
	b.PublishSegment(ctx, earlyStreamer, "playback-source", &bus.Seg{Muxl: segments[0], Published: false})
	publishEarly(shortest)
	early, err := requestMoqBroadcast(ctx, client, "playback/"+earlyStreamer)
	require.NoError(t, err)
	requireBatch := func(broadcast *official.BroadcastConsumer, duration time.Duration) {
		catalog, err := broadcast.Catalog(ctx)
		require.NoError(t, err)
		require.Contains(t, catalog.Sections, "streamplace", "browser catalog carries actual publication buffering before playback begins")
		var timing struct {
			BatchDurationMs int64 `json:"batchDurationMs"`
		}
		require.NoError(t, json.Unmarshal([]byte(catalog.Sections["streamplace"]), &timing))
		require.Positive(t, timing.BatchDurationMs)
		require.Equal(t, int64((duration+time.Millisecond-1)/time.Millisecond), timing.BatchDurationMs)
	}
	requireBatch(early, packets[shortest].BatchDuration)
	earlyGot := make(chan []byte, 2)
	earlyDone := make(chan error, 1)
	go func() {
		earlyDone <- consumeBroadcast(ctx, early,
			func(segment []byte) error { earlyGot <- segment; return nil },
			func(context.Context, []byte) error { return nil })
	}()
	readEarly := func(i int) {
		select {
		case payload := <-earlyGot:
			require.Equal(t, segments[i], payload, "browser playback serves only published source media before codec completion")
		case err := <-earlyDone:
			t.Fatalf("browser playback ended before the source arrived: %v", err)
		case <-ctx.Done():
			t.Fatal("browser playback waited for canonical codec completion")
		}
	}
	readEarly(shortest)
	publishEarly(longest)
	readEarly(longest)
	requireBatch(early, packets[longest].BatchDuration)
	publishEarly(shortest)
	readEarly(shortest)
	requireBatch(early, packets[longest].BatchDuration)
	b.EndSession(ctx, earlyStreamer, media.PlaybackSourceRendition)
	select {
	case err := <-earlyDone:
		require.Error(t, err)
		require.False(t, errors.Is(err, context.DeadlineExceeded), "browser playback ends with the playback session")
	case <-ctx.Done():
		t.Fatal("browser playback outlived its session")
	}
	publishEarly(shortest)
	restartedClient, err := r.dialMoq(ctx, moqURL)
	require.NoError(t, err)
	defer restartedClient.Close()
	restarted, err := requestMoqBroadcast(ctx, restartedClient, "playback/"+earlyStreamer)
	require.NoError(t, err)
	requireBatch(restarted, packets[shortest].BatchDuration)
	b.EndSession(ctx, earlyStreamer, media.PlaybackSourceRendition)

	b.EndSession(ctx, streamer, moqSourceChannel)
	select {
	case err := <-done:
		require.Error(t, err)
		require.False(t, errors.Is(err, context.DeadlineExceeded), "the broadcast ends with the stream")
	case <-ctx.Done():
		t.Fatal("the broadcast outlived its stream")
	}
}

// A peer's group is refused once it grows past the cap, before the node
// holds more of what nothing has verified yet.
func TestMoqGroupIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	limit := moqMaxGroupBytes
	moqMaxGroupBytes = 16
	t.Cleanup(func() { moqMaxGroupBytes = limit })
	broadcast, err := official.NewBroadcastProducer()
	require.NoError(t, err)
	defer broadcast.Close()
	track, err := broadcast.PublishTrack("video1", nil)
	require.NoError(t, err)
	read := func(frames ...[]byte) error {
		consumer, err := track.Consume(nil)
		require.NoError(t, err)
		defer consumer.Cancel()
		group, err := track.AppendGroup()
		require.NoError(t, err)
		for _, frame := range frames {
			require.NoError(t, group.WriteFrame(official.Frame{Payload: frame}))
		}
		require.NoError(t, group.Finish())
		g, err := consumer.NextGroup(ctx)
		require.NoError(t, err)
		_, err = readMoqGroup(ctx, g)
		return err
	}
	frame := []byte{0, 0, 0, 8, 'u', 'u', 'i', 'd'}
	require.NoError(t, read(frame, frame), "at the cap")
	require.ErrorContains(t, read(frame, frame, frame), "over 16 bytes")
}
