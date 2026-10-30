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

func TestMoqPlaybackGroupsFollowVideoSyncFrames(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	segments := signedSegments(t, ctx)
	video := make([][][]byte, 2)
	var init bytes.Buffer
	for i := range video {
		tracks, err := media.MuxlTracks(ctx, segments[i])
		require.NoError(t, err)
		video[i], _, err = cmafFragments(tracks["video1"])
		require.NoError(t, err)
		require.Greater(t, len(video[i]), 3, "the real GoP contains key and delta fragments")
		if i == 0 {
			require.NoError(t, muxl.RunMuxlWrapInit(ctx, bytes.NewReader(tracks["video1"]), &init))
		}
	}
	// Re-sign real encoded fragments at admission boundaries without changing
	// their sample flags or timestamps. Each output's signature covers its own
	// exact bytes, including portions that start between video keyframes.
	sign := func(fragments [][]byte, startsWithSync bool) []byte {
		input := bytes.Join(append([][]byte{init.Bytes()}, fragments...), nil)
		signer, err := media.NewEphemeralMediaSigner("test-streamer")
		require.NoError(t, err)
		events := make(chan *muxl.MuxlEvent, 16)
		done := make(chan error, 1)
		go func() {
			done <- signer.SignSegmentStream(ctx, bytes.NewReader(input), events)
			close(events)
		}()
		var signed []*muxl.MuxlEvent
		for event := range events {
			if event.Type == "signed-segment" {
				signed = append(signed, event)
			}
		}
		require.NoError(t, <-done)
		require.Len(t, signed, 1)
		require.Equal(t, startsWithSync, slices.Contains(signed[0].Samples["1"].SyncIndices, uint32(1)))
		require.NotEmpty(t, signed[0].Tracks["1"])
		return signed[0].Tracks["1"]
	}
	opening := sign(video[0][:2], true)
	continuation := sign(video[0][2:], false)
	initialDelta := sign(video[0][1:], false)
	nextGoP := sign(video[1], true)
	hidden := sign(video[0][2:3], false)
	afterHidden := sign(video[0][3:], false)
	interval := moqLivenessInterval
	moqLivenessInterval = 10 * time.Millisecond
	t.Cleanup(func() { moqLivenessInterval = interval })
	for _, input := range []struct {
		name   string
		first  []byte
		second []byte
		hidden []byte
		groups [][]byte
	}{
		{
			name:   "GoP crosses admission batches",
			first:  opening,
			second: bytes.Join([][]byte{continuation, nextGoP}, nil),
			groups: [][]byte{bytes.Join([][]byte{opening, continuation}, nil), nextGoP},
		},
		{
			name:   "publication gap waits for sync",
			first:  opening,
			hidden: hidden,
			second: bytes.Join([][]byte{afterHidden, nextGoP}, nil),
			groups: [][]byte{opening, nextGoP},
		},
		{
			name:   "mid-GoP join waits for sync",
			first:  initialDelta,
			second: nextGoP,
			groups: [][]byte{nextGoP},
		},
	} {
		for _, ending := range []string{"session end", "cancel"} {
			t.Run(input.name+"/"+ending, func(t *testing.T) {
				serveCtx, stop := context.WithCancel(ctx)
				defer stop()
				b := bus.NewBus()
				const streamer = "did:plc:video-groups"
				publish := func(segment []byte, published bool) {
					b.PublishSegment(ctx, streamer, media.PlaybackSourceRendition, &bus.Seg{
						Muxl: segment, BatchDuration: time.Second, Published: published,
					})
				}
				publish(input.first, true)
				origin := official.NewOriginProducer()
				requests, err := origin.Dynamic("", official.Route{})
				require.NoError(t, err)
				defer requests.Cancel()
				done := make(chan struct{})
				r := &WebsocketReplicator{bus: b, cli: &config.CLI{}}
				go func() {
					defer close(done)
					request, err := requests.RequestedBroadcast(serveCtx)
					if err == nil {
						r.serveBroadcast(serveCtx, request)
					}
				}()
				defer func() {
					stop()
					<-done
				}()
				consumer, err := origin.Consume().RequestBroadcast(ctx, "playback/"+streamer)
				require.NoError(t, err)
				track, err := subscribeMoqTrack(ctx, consumer, "video1", moqSourcePriority)
				require.NoError(t, err)
				defer track.Cancel()
				if len(input.hidden) > 0 {
					publish(input.hidden, false)
				}
				publish(input.second, true)
				for i, want := range input.groups {
					group, err := track.NextGroup(ctx)
					require.NoError(t, err)
					require.NotNil(t, group)
					first, err := group.ReadFrame(ctx)
					require.NoError(t, err)
					require.NotNil(t, first)
					if i == len(input.groups)-1 {
						if ending == "session end" {
							b.EndSession(ctx, streamer, media.PlaybackSourceRendition)
						} else {
							stop()
						}
						select {
						case <-done:
						case <-ctx.Done():
							t.Fatal("playback publication outlived its session")
						}
					}
					got := bytes.Clone(first.Payload)
					for {
						frame, err := group.ReadFrame(ctx)
						require.NoError(t, err, "reading group %d after %d bytes", i, len(got))
						if frame == nil {
							break
						}
						got = append(got, frame.Payload...)
					}
					group.Cancel()
					require.True(t, bytes.Equal(want, got), "group %d must preserve one real GoP's signed bytes in order: got %d bytes, want %d", i, len(got), len(want))
				}
			})
		}
	}
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
	gotRendition := next(renditions)
	require.True(t, bytes.Equal(rendition, gotRendition), "a rendition travels on its own track: got %d bytes, want %d", len(gotRendition), len(rendition))

	// Browser playback can start from the verified source while canonical
	// codec completion is still pending on the peer replication channel.
	const earlyStreamer = "did:plc:early"
	durations := make([]time.Duration, len(segments))
	shortest, longest := 0, 0
	for i, segment := range segments {
		var flat bytes.Buffer
		require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(segment), "flat", &flat))
		metadata, err := media.ParseSegmentMediaData(ctx, flat.Bytes())
		require.NoError(t, err)
		durations[i] = metadata.BatchDuration
		if durations[i] < durations[shortest] {
			shortest = i
		}
		if durations[i] > durations[longest] {
			longest = i
		}
	}
	require.Less(t, durations[shortest], durations[longest], "fixture includes a shorter final batch")
	publishEarly := func(i int) {
		b.PublishSegment(ctx, earlyStreamer, media.PlaybackSourceRendition, &bus.Seg{Muxl: segments[i], BatchDuration: durations[i], Published: true})
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
	requireBatch(early, durations[shortest])
	earlyGot := map[string]chan []byte{}
	earlyDone := make(chan error, 2)
	for _, name := range []string{"video1", "audio2"} {
		track, err := subscribeMoqTrack(ctx, early, name, moqSourcePriority)
		require.NoError(t, err)
		frames := make(chan []byte, 2)
		earlyGot[name] = frames
		go func() {
			forward := func() error {
				defer track.Cancel()
				for group, err := range track.Groups(ctx) {
					if err != nil {
						return err
					}
					for frame, err := range group.Frames(ctx) {
						if err != nil {
							group.Cancel()
							return err
						}
						select {
						case frames <- frame.Payload:
						case <-ctx.Done():
							group.Cancel()
							return ctx.Err()
						}
					}
					group.Cancel()
				}
				return errors.New("playback track ended")
			}
			earlyDone <- forward()
		}()
	}
	readEarly := func(i int) {
		tracks, err := media.MuxlTracks(ctx, segments[i])
		require.NoError(t, err)
		for name, want := range tracks {
			var got []byte
			for len(got) < len(want) {
				select {
				case payload := <-earlyGot[name]:
					got = append(got, payload...)
				case err := <-earlyDone:
					t.Fatalf("browser playback ended before the source arrived: %v", err)
				case <-ctx.Done():
					t.Fatal("browser playback waited for canonical codec completion")
				}
			}
			require.Equal(t, want, got, "browser playback serves only published source media before codec completion")
		}
	}
	readEarly(shortest)
	publishEarly(longest)
	readEarly(longest)
	requireBatch(early, durations[longest])
	publishEarly(shortest)
	readEarly(shortest)
	requireBatch(early, durations[longest])
	b.EndSession(ctx, earlyStreamer, media.PlaybackSourceRendition)
	for range earlyGot {
		select {
		case err := <-earlyDone:
			require.Error(t, err)
			require.False(t, errors.Is(err, context.DeadlineExceeded), "browser playback ends with the playback session")
		case <-ctx.Done():
			t.Fatal("browser playback outlived its session")
		}
	}
	publishEarly(shortest)
	restartedClient, err := r.dialMoq(ctx, moqURL)
	require.NoError(t, err)
	defer restartedClient.Close()
	restarted, err := requestMoqBroadcast(ctx, restartedClient, "playback/"+earlyStreamer)
	require.NoError(t, err)
	requireBatch(restarted, durations[shortest])
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
