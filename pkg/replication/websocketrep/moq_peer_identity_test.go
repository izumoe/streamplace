package websocketrep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	official "moq.dev/moq"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/muxl"
	"stream.place/streamplace/pkg/placestream"
)

func TestMoqPeerPreservesPrimarySourcePolicy(t *testing.T) {
	// Bound media operations after the cold WASM compilation.
	require.NoError(t, muxl.Initialize())
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	segments := signedSegments(t, ctx)
	base, err := media.MuxlTracks(ctx, segments[0])
	require.NoError(t, err)
	remap := func(data []byte, from, to uint32) []byte {
		var fmp4 bytes.Buffer
		require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(data), "fmp4", &fmp4))
		canonical, err := muxl.RunMuxlCanonicalize(ctx, fmp4.Bytes(), map[uint32]uint32{from: to})
		require.NoError(t, err)
		tracks, err := media.MuxlTracks(ctx, canonical)
		require.NoError(t, err)
		require.Len(t, tracks, 1)
		for _, track := range tracks {
			return track
		}
		return nil
	}
	higherVideo := remap(base["video1"], 1, 2)
	audio := remap(base["audio2"], 2, 3)
	signer, err := media.NewEphemeralMediaSigner("peer-policy-source")
	require.NoError(t, err)
	sign := func(t *testing.T, signer *media.MediaSignerLocal, data []byte, published bool, allowedBroadcasters []string, livestream *placestream.Livestream) []byte {
		t.Helper()
		title := "Peer policy source"
		if livestream != nil {
			title = livestream.Title
		}
		actions := []map[string]string{{"action": "c2pa.created"}}
		if published {
			actions = append(actions, map[string]string{"action": "c2pa.published"})
		}
		assertions := []map[string]any{
			{"label": "c2pa.actions", "data": map[string]any{"actions": actions}},
			{"label": "cawg.metadata", "data": map[string]any{
				"@context":   map[string]string{"dc": "http://purl.org/dc/elements/1.1/"},
				"dc:creator": signer.DID(), "dc:title": title,
				"dc:date": "1970-01-01T00:00:00.000Z",
			}},
		}
		if livestream != nil {
			assertions = append(assertions, map[string]any{"label": "place.stream.livestream", "data": livestream})
		}
		if allowedBroadcasters != nil {
			assertions = append(assertions, map[string]any{
				"label": "place.stream.metadata.configuration",
				"data":  map[string]any{"distributionPolicy": map[string]any{"allowedBroadcasters": allowedBroadcasters}},
			})
		}
		signer.PrebuiltManifest, err = json.Marshal(map[string]any{
			"title":      "Peer policy source",
			"assertions": assertions,
		})
		require.NoError(t, err)
		var fmp4 bytes.Buffer
		require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(data), "fmp4", &fmp4))
		events := make(chan *muxl.MuxlEvent, 16)
		done := make(chan error, 1)
		go func() {
			done <- signer.SignSegmentStream(ctx, bytes.NewReader(fmp4.Bytes()), events)
			close(events)
		}()
		var signed []byte
		for event := range events {
			if event.Type != "signed-segment" {
				continue
			}
			ids := make([]string, 0, len(event.Tracks))
			for id := range event.Tracks {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			for _, id := range ids {
				signed = append(signed, event.Tracks[id]...)
			}
		}
		require.NoError(t, <-done)
		require.NotEmpty(t, signed)
		return signed
	}
	// A batch may start with a higher-ID track before the next piece supplies
	// the lowest-ID video. Both pieces keep their original signed policy.
	higherPublic := sign(t, signer, higherVideo, true, nil, nil)
	higherPrivate := sign(t, signer, higherVideo, false, nil, nil)
	higherRestricted := sign(t, signer, higherVideo, true, []string{"did:web:restricted.example"}, nil)
	lower := bytes.Join([][]byte{base["video1"], audio}, nil)
	public := sign(t, signer, lower, true, nil, nil)
	private := sign(t, signer, lower, false, nil, nil)
	seenAt := "2026-10-30T00:00:01.000Z"
	heartbeatAt := "2026-10-30T00:00:02.000Z"
	live := placestream.Livestream{Title: "Peer policy source", CreatedAt: "2026-10-30T00:00:00.000Z", LastSeenAt: &seenAt}
	heartbeat := live
	heartbeat.LastSeenAt = &heartbeatAt
	retitled := heartbeat
	retitled.Title = "Updated stream title"
	higherLive := sign(t, signer, higherVideo, true, nil, &live)
	lowerHeartbeat := sign(t, signer, lower, true, nil, &heartbeat)
	lowerRetitled := sign(t, signer, lower, true, nil, &retitled)
	nodeSigner, err := media.NewEphemeralMediaSigner("peer-policy-node")
	require.NoError(t, err)
	node := sign(t, nodeSigner, remap(base["video1"], 1, 100), false, nil, nil)
	t.Run("independent node metadata", func(t *testing.T) {
		valid, err := media.ValidateMP4MediaC2PA(ctx, bytes.Join([][]byte{node, public}, nil))
		require.NoError(t, err)
		primary, err := media.ValidateMP4MediaC2PA(ctx, public)
		require.NoError(t, err)
		require.True(t, valid.Meta.Published, "node-signed addenda must not change the source's publication policy")
		require.Equal(t, *primary.Manifest.Label, *valid.Manifest.Label)
	})
	for _, input := range []struct {
		name      string
		first     []byte
		lowest    []byte
		published bool
	}{
		{"published source", higherPublic, public, true},
		{"private to public", higherPrivate, public, false},
		{"public to private", higherPublic, private, false},
		{"distribution policy change", higherRestricted, public, false},
		{"livestream heartbeat", higherLive, lowerHeartbeat, true},
		{"stream title update", higherLive, lowerRetitled, true},
	} {
		t.Run(input.name, func(t *testing.T) {
			source := bytes.Join([][]byte{input.first, input.lowest}, nil)
			before, err := media.ValidateMP4MediaC2PA(ctx, source)
			require.NoError(t, err)
			primary, err := media.ValidateMP4MediaC2PA(ctx, input.lowest)
			require.NoError(t, err)
			producer, err := official.NewBroadcastProducer()
			require.NoError(t, err)
			defer producer.Close()
			consumer, err := producer.Consume()
			require.NoError(t, err)
			tracks := moqTracks{}
			defer tracks.finish()
			require.NoError(t, tracks.publish(ctx, producer, source, func(track *official.CMAFProducer) (*official.CMAFGroupProducer, error) {
				return track.CreateGroup(0)
			}))
			finished := errors.New("received source batch")
			var received []byte
			err = consumeBroadcast(ctx, consumer, func(segment []byte) error {
				received = segment
				return finished
			}, func(context.Context, []byte) error { return nil })
			require.ErrorIs(t, err, finished)
			wantTracks, err := media.MuxlTracks(ctx, source)
			require.NoError(t, err)
			gotTracks, err := media.MuxlTracks(ctx, received)
			require.NoError(t, err)
			require.Equal(t, len(wantTracks), len(gotTracks))
			for name, want := range wantTracks {
				require.True(t, bytes.Equal(want, gotTracks[name]), "track %s must preserve its exact signed bytes", name)
			}
			after, err := media.ValidateMP4MediaC2PA(ctx, received)
			require.NoError(t, err)
			t.Logf("source label=%s published=%t; peer label=%s published=%t", *before.Manifest.Label, before.Meta.Published, *after.Manifest.Label, after.Meta.Published)
			require.Equal(t, input.published, before.Meta.Published, "mixed source policies must keep the admission batch private")
			require.Equal(t, input.published, after.Meta.Published, "replication must preserve the batch's effective policy")
			require.Equal(t, *primary.Manifest.Label, *before.Manifest.Label, "the lowest track's first asset is the stable source deduplication identity")
			require.Equal(t, *before.Manifest.Label, *after.Manifest.Label, "the primary source deduplication identity must survive replication")
			require.Equal(t, primary.Meta.MetadataConfiguration, before.Meta.MetadataConfiguration, "the primary asset's signed metadata stays intact")
			require.Equal(t, primary.Meta.Title, before.Meta.Title)
			require.Equal(t, primary.Meta.Livestream, before.Meta.Livestream)
		})
	}
}
