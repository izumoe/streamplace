package websocketrep

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	official "moq.dev/moq"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/muxl"
)

func TestMoqDrainsPartialEOF(t *testing.T) {
	require.NoError(t, muxl.Initialize())
	ctx := t.Context()
	fixture, err := os.ReadFile("../../../test/fixtures/h264-opus-frag.mp4")
	require.NoError(t, err)
	higherCanonical, err := muxl.RunMuxlCanonicalize(ctx, fixture, map[uint32]uint32{1: 2, 2: 3})
	require.NoError(t, err)
	var higherInput bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(higherCanonical), "fmp4", &higherInput))
	signer, err := media.NewEphemeralMediaSigner("partial-eof-source")
	require.NoError(t, err)
	sign := func(input []byte) [][]byte {
		t.Helper()
		events := make(chan *muxl.MuxlEvent, 16)
		finished := make(chan error, 1)
		go func() {
			finished <- signer.SignSegmentStream(ctx, bytes.NewReader(input), events)
			close(events)
		}()
		var segments [][]byte
		for event := range events {
			if event.Type != "signed-segment" {
				continue
			}
			ids := make([]string, 0, len(event.Tracks))
			for id := range event.Tracks {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			var segment []byte
			for _, id := range ids {
				segment = append(segment, event.Tracks[id]...)
			}
			segments = append(segments, segment)
		}
		require.NoError(t, <-finished)
		require.GreaterOrEqual(t, len(segments), 2)
		return segments
	}
	lowerCanonical, err := muxl.RunMuxlCanonicalize(ctx, fixture, map[uint32]uint32{1: 1, 2: 3})
	require.NoError(t, err)
	var lowerInput bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(lowerCanonical), "fmp4", &lowerInput))
	lower := sign(lowerInput.Bytes())
	higher := sign(higherInput.Bytes())
	higherTracks, err := media.MuxlTracks(ctx, higher[0])
	require.NoError(t, err)
	first := bytes.Join([][]byte{lower[0], higherTracks["video2"]}, nil)
	tail := higher[1]
	tailTracks, err := media.MuxlTracks(ctx, tail)
	require.NoError(t, err)
	require.Empty(t, tailTracks["video1"])
	require.NotEmpty(t, tailTracks["video2"])
	require.NotEmpty(t, tailTracks["audio3"])
	_, err = media.ValidateMP4MediaC2PA(ctx, tail)
	require.NoError(t, err)
	for _, mode := range []string{"source tracks", "broadcast closes", "source track added"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
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
				require.NoError(t, tracks.publish(ctx, producer, data, func(track *official.CMAFProducer) (*official.CMAFGroupProducer, error) {
					return track.CreateGroup(sequence)
				}))
			}
			publish(first, 0)
			received := make(chan []byte, 2)
			done := make(chan error, 1)
			finished := errors.New("received final source batch")
			go func() {
				count := 0
				handle := func(data []byte) error {
					received <- data
					count++
					if count == 2 {
						return finished
					}
					return nil
				}
				if mode != "source tracks" {
					done <- consumeBroadcast(ctx, consumer, handle, func(context.Context, []byte) error { return nil })
				} else {
					done <- consumeSource(ctx, consumer, []string{"video1", "video2", "audio3"}, handle)
				}
			}()
			select {
			case <-received:
			case err := <-done:
				t.Fatalf("first source batch failed: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if mode == "source track added" {
				_, _, err := tracks.track(ctx, producer, "video4", higherTracks["video2"], nil)
				require.NoError(t, err)
				select {
				case err := <-done:
					require.ErrorContains(t, err, "source tracks changed")
				case <-ctx.Done():
					t.Fatal("a new source track requires a new session")
				}
				return
			}
			// The first real batch has already reached the peer, so every source
			// subscription exists before only video2 and audio3 publish their tail.
			publish(tail, 1)
			tracks.finish()
			if mode == "broadcast closes" {
				require.NoError(t, producer.Close())
			}
			select {
			case err := <-done:
				require.ErrorIs(t, err, finished, "a finished video1 cannot discard accepted video2/audio3 source media")
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			got := <-received
			gotTracks, err := media.MuxlTracks(ctx, got)
			require.NoError(t, err)
			require.Len(t, gotTracks, len(tailTracks))
			for name, want := range tailTracks {
				require.True(t, bytes.Equal(want, gotTracks[name]), "track %s must preserve its exact signed tail", name)
			}
			_, err = media.ValidateMP4MediaC2PA(ctx, got)
			require.NoError(t, err)
		})
	}
}
