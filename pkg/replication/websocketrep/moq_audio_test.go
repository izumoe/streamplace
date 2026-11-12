package websocketrep

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	official "moq.dev/moq"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/crypto/signers"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/muxl"
)

func TestMoqDeliversAudioContinuationBeforeVideoEnds(t *testing.T) {
	for _, audioID := range []uint32{2, 65539} {
		t.Run(fmt.Sprint(audioID), func(t *testing.T) {
			ctx := t.Context()
			sourceSigner, err := media.NewEphemeralMediaSigner("source-audio")
			require.NoError(t, err)
			source := signedSegmentsWithRemap(t, ctx, sourceSigner, map[uint32]uint32{2: audioID})[0]
			audioName := fmt.Sprintf("audio%d", audioID)
			wireID := audioID
			if wireID >= 1<<16 {
				wireID -= 1 << 16
			}
			wireName := fmt.Sprintf("audio%d", wireID)
			parts, err := media.MuxlTracks(ctx, source)
			require.NoError(t, err)
			signer, err := media.NewEphemeralMediaSigner("audio-tail")
			require.NoError(t, err)
			key, err := signers.MarshalES256KPrivateKeyPEM(signer.Signer)
			require.NoError(t, err)
			tail, err := muxl.RunMuxlSignTranscode(ctx, muxl.TranscodeInput{
				Output: parts[audioName], Source: parts[audioName], CertPEM: signer.Cert, KeyPEM: key,
				Manifest: []byte(`{"title":"transcoded audio tail","assertions":[{"label":"c2pa.actions","data":{"actions":[{"action":"c2pa.transcoded","parameters":{"org.cai.ingredientIds":["muxl.source"]}}]}}]}`),
			})
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			producer, err := official.NewBroadcastProducer()
			require.NoError(t, err)
			defer producer.Close()
			consumer, err := producer.Consume()
			require.NoError(t, err)
			tracks := moqTracks{}
			defer tracks.finish()
			publish := func(data []byte, sequence uint64) {
				t.Helper()
				require.NoError(t, tracks.publish(ctx, producer, data, false, func(track *official.CMAFProducer) (*official.CMAFGroupProducer, error) {
					return track.CreateGroup(sequence)
				}))
			}
			publish(source, 0)

			received := make(chan []byte, 2)
			addenda := make(chan []byte, 1)
			done := make(chan error, 1)
			go func() {
				done <- consumeBroadcast(ctx, consumer, func(data []byte) error {
					if _, err := media.ValidateMP4MediaC2PA(ctx, data); err != nil {
						return err
					}
					select {
					case received <- data:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}, func(_ context.Context, data []byte) error { addenda <- data; return nil })
			}()
			select {
			case got := <-received:
				require.True(t, bytes.Equal(source, got), "the source retains its original signed audio")
			case err := <-done:
				t.Fatalf("source failed: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			require.NoError(t, tracks.publishAudioAddendum(ctx, producer, tail))
			select {
			case got := <-addenda:
				require.Equal(t, tail, got)
			case err := <-done:
				t.Fatal(err)
			case <-ctx.Done():
				t.Fatal("audio continuation waited for a future video group")
			}
			catalog, err := consumer.Catalog(ctx)
			require.NoError(t, err)
			sources, renditions := moqTrackSets(catalog)
			require.Equal(t, []string{"video1", wireName}, sources, "audio continuations cannot become audio-only source batches on older peers")
			require.Equal(t, []string{fmt.Sprintf("audio%d", media.RenditionTrackID(int(wireID)))}, renditions)
			// A subsequent source keeps its original sequence and full validation.
			publish(source, 1)
			select {
			case got := <-received:
				require.Equal(t, source, got)
			case err := <-done:
				t.Fatal(err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			cancel()
			require.Error(t, <-done)

			// Late subscribers replay the canonical source and its typed audio event in
			// the same FIFO, rather than racing separate source/rendition channels.
			replayCtx, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			b := bus.NewBus()
			b.PublishSegment(replayCtx, "did:test:audio", moqSourceChannel, &bus.Seg{Filepath: "last-source", Muxl: source, Published: true})
			b.PublishSegment(replayCtx, "did:test:audio", moqSourceChannel, &bus.Seg{Filepath: "last-source", Muxl: tail, Published: true, AudioOnly: true})
			replay, err := official.NewBroadcastProducer()
			require.NoError(t, err)
			defer replay.Close()
			r := &WebsocketReplicator{bus: b}
			ready := make(chan struct{})
			served := make(chan struct{})
			go func() {
				r.serveTracks(replayCtx, "did:test:audio", moqSourceChannel, replay, false, func() error { close(ready); return nil })
				close(served)
			}()
			defer func() { stop(); <-served }()
			select {
			case <-ready:
			case <-replayCtx.Done():
				t.Fatal(replayCtx.Err())
			}
			peer, err := replay.Consume()
			require.NoError(t, err)
			replaySource := make(chan []byte, 1)
			replayAudio := make(chan []byte, 1)
			replayDone := make(chan error, 1)
			go func() {
				replayDone <- consumeBroadcast(replayCtx, peer, func(data []byte) error { replaySource <- data; return nil }, func(_ context.Context, data []byte) error { replayAudio <- data; return nil })
			}()
			select {
			case got := <-replaySource:
				require.Equal(t, source, got)
			case err := <-replayDone:
				t.Fatal(err)
			case <-replayCtx.Done():
				t.Fatal(replayCtx.Err())
			}
			select {
			case got := <-replayAudio:
				require.Equal(t, tail, got)
			case err := <-replayDone:
				t.Fatal(err)
			case <-replayCtx.Done():
				t.Fatal(replayCtx.Err())
			}
			stop()
			require.Error(t, <-replayDone)

		})
	}
}

func TestMoqLegacyOmitsAudioContinuation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	source := signedSegments(t, ctx)[0]
	parts, err := media.MuxlTracks(ctx, source)
	require.NoError(t, err)
	b := bus.NewBus()
	const streamer = "did:test:legacy-audio"
	publish := func(data []byte, tail bool) {
		b.PublishSegment(ctx, streamer, moqSourceChannel, &bus.Seg{Filepath: "source", Muxl: data, Published: true, AudioOnly: tail})
	}
	publish(source, false)
	producer, err := official.NewBroadcastProducer()
	require.NoError(t, err)
	defer producer.Close()
	ready := make(chan struct{})
	done := make(chan struct{})
	r := &WebsocketReplicator{bus: b}
	go func() {
		defer close(done)
		r.serveTracks(ctx, streamer, moqSourceChannel, producer, true, func() error { close(ready); return nil })
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	peer, err := producer.Consume()
	require.NoError(t, err)
	received := make(chan []byte, 2)
	consumed := make(chan error, 1)
	go func() {
		consumed <- consumeSource(ctx, peer, []string{"video1", "audio2"}, func(data []byte) error { received <- data; return nil })
	}()
	defer func() { cancel(); require.ErrorIs(t, <-consumed, context.Canceled) }()
	receive := func() {
		t.Helper()
		select {
		case got := <-received:
			require.True(t, bytes.Equal(source, got), "original signed source is unchanged")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	receive()
	publish(parts["audio2"], true)
	publish(source, false)
	receive()
	catalog, err := peer.Catalog(ctx)
	require.NoError(t, err)
	require.Len(t, catalog.Audio, 1, "legacy peers must not receive a node-signed continuation track")
	require.Contains(t, catalog.Audio, "audio2", "an original Opus source remains available")
}

func TestMoqAudioAliasesDoNotCollideWithExistingSourceNames(t *testing.T) {
	ctx := t.Context()
	signer, err := media.NewEphemeralMediaSigner("mixed-audio-namespace")
	require.NoError(t, err)
	low := signedSegmentsWithRemap(t, ctx, signer, nil)[0]
	high := signedSegmentsWithRemap(t, ctx, signer, map[uint32]uint32{2: 65538})[0]
	highTracks, err := media.MuxlTracks(ctx, high)
	require.NoError(t, err)
	source := append(bytes.Clone(low), highTracks["audio65538"]...)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	producer, err := official.NewBroadcastProducer()
	require.NoError(t, err)
	defer producer.Close()
	tracks := moqTracks{}
	defer tracks.finish()
	require.NoError(t, tracks.publish(ctx, producer, source, false, func(track *official.CMAFProducer) (*official.CMAFGroupProducer, error) { return track.CreateGroup(0) }))
	consumer, err := producer.Consume()
	require.NoError(t, err)
	catalog, err := consumer.Catalog(ctx)
	require.NoError(t, err)
	require.Contains(t, catalog.Audio, "audio2")
	require.Contains(t, catalog.Audio, "audio65538", "a foreign mixed namespace must retain both independent tracks")
	sources, addenda := moqTrackSets(catalog)
	require.Equal(t, []string{"video1", "audio2"}, sources)
	require.Equal(t, []string{"audio65538"}, addenda)
}
