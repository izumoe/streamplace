package media

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
)

func TestAudioAddendumPreservesSourceAndAdmission(t *testing.T) {
	ctx := t.Context()
	cert, key := nodeSignerForTest(t)
	ms := audioSignerForTest(t)
	sources := allSignedBareSegments(t, ctx, ms, getFixture("ertmp-red-h264.mp4"))
	encoder := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
	var completed [][]byte
	var tail []byte
	tr := encoder.newStreamTranscoder(ctx, "opus", cert, key, func(_ any, data []byte, audioOnly bool) {
		if audioOnly {
			tail = data
		} else {
			completed = append(completed, data)
		}
	})
	for _, source := range sources {
		require.NoError(t, tr.Feed(source, nil))
	}
	require.NoError(t, tr.Close())
	require.NotEmpty(t, tail, "EOS must deliver the encoder's retained packets")
	tailEvents, err := unwrapMuxlEvents(ctx, tail)
	require.NoError(t, err)
	require.Nil(t, catalogFromEvents(tailEvents).Video, "the addendum must not replay video")

	did := ms.Streamer()
	mm, controller := publisherAccessForTest(t, did)
	admitted, err := mm.admitPublisher(ctx, did)
	require.NoError(t, err)
	generation, err := mm.checkPublisherContext(admitted, did)
	require.NoError(t, err)
	for _, data := range completed {
		valid, err := ValidateMP4MediaC2PA(ctx, data)
		require.NoError(t, err)
		mm.bufferSegment(did, valid.Meta.StartTime.Time(), true, true, data, generation, *valid.Manifest.Label)
	}
	before := mm.BufferedSegments(did)
	notifications := mm.NewSegment()
	require.Error(t, mm.ReceiveAudioAddendum(admitted, "did:test:someone-else", tail))
	corrupt := bytes.Clone(tail)
	corrupt[len(corrupt)-1] ^= 1
	require.Error(t, mm.ReceiveAudioAddendum(admitted, did, corrupt), "a bad audio signature cannot reach playback")
	require.Equal(t, before, mm.BufferedSegments(did))
	require.NoError(t, mm.ReceiveAudioAddendum(admitted, did, tail))
	select {
	case notification := <-notifications:
		require.True(t, notification.AudioOnly)
		require.Equal(t, tail, notification.Muxl)
		require.True(t, notification.Metadata.Published)
	case <-time.After(time.Second):
		t.Fatal("tail was not queued for ordered delivery")
	}
	after := mm.BufferedSegments(did)
	require.Len(t, after, len(before))
	require.Equal(t, append(bytes.Clone(before[len(before)-1].Data), tail...), after[len(after)-1].Data)
	require.True(t, bytes.HasSuffix(before[len(before)-1].Data, tail) == false, "captured clip buffers remain immutable")
	require.Error(t, mm.ReceiveAudioAddendum(admitted, did, tail), "replayed audio must not be appended twice")
	revokeAndReapprovePublisher(t, controller, did)
	require.ErrorContains(t, mm.ReceiveAudioAddendum(context.WithoutCancel(admitted), did, tail), "reconnect")
	// A fresh admission also cannot release an old session's retained tail.
	fresh, err := mm.admitPublisher(ctx, did)
	require.NoError(t, err)
	require.Error(t, mm.ReceiveAudioAddendum(fresh, did, tail))
}

// A reconnect can reuse the same decode timestamps. An old tail's ingredients
// must not borrow the fresh session's public visibility or admission generation.
func TestAudioAddendumRejectsEarlierSessionAtSameMediaClock(t *testing.T) {
	ctx := t.Context()
	ms := audioSignerForTest(t)
	old, tail := audioAddendumSourceForTest(t, ms)
	current, _ := audioAddendumSourceForTest(t, ms)
	did := ms.Streamer()
	mm, controller := publisherAccessForTest(t, did)
	admitted, err := mm.admitPublisher(ctx, did)
	require.NoError(t, err)
	oldGeneration, err := mm.checkPublisherContext(admitted, did)
	require.NoError(t, err)
	for _, data := range old {
		valid, err := ValidateMP4MediaC2PA(ctx, data)
		require.NoError(t, err)
		mm.bufferSegment(did, valid.Meta.StartTime.Time(), false, true, data, oldGeneration, *valid.Manifest.Label)
	}
	revokeAndReapprovePublisher(t, controller, did)
	fresh, err := mm.admitPublisher(ctx, did)
	require.NoError(t, err)
	generation, err := mm.checkPublisherContext(fresh, did)
	require.NoError(t, err)
	for _, data := range current {
		valid, err := ValidateMP4MediaC2PA(ctx, data)
		require.NoError(t, err)
		mm.bufferSegment(did, valid.Meta.StartTime.Time(), true, true, data, generation, *valid.Manifest.Label)
	}
	before := mm.BufferedSegments(did)
	require.Error(t, mm.ReceiveAudioAddendum(fresh, did, tail), "old ingredients cannot take a new session's matching audio clock")
	require.Equal(t, before, mm.BufferedSegments(did))
}

func TestAudioAddendumKeepsPrivateContributorPrivate(t *testing.T) {
	ctx := t.Context()
	ms := audioSignerForTest(t)
	completed, tail := audioAddendumSourceForTest(t, ms)
	require.GreaterOrEqual(t, len(completed), 2)
	did := ms.Streamer()
	mm, _ := publisherAccessForTest(t, did)
	admitted, err := mm.admitPublisher(ctx, did)
	require.NoError(t, err)
	generation, err := mm.checkPublisherContext(admitted, did)
	require.NoError(t, err)
	for i, data := range completed {
		valid, err := ValidateMP4MediaC2PA(ctx, data)
		require.NoError(t, err)
		mm.bufferSegment(did, valid.Meta.StartTime.Time(), i == len(completed)-1, true, data, generation, *valid.Manifest.Label)
	}
	notifications := mm.NewSegment()
	require.NoError(t, mm.ReceiveAudioAddendum(admitted, did, tail))
	select {
	case notification := <-notifications:
		require.False(t, notification.Metadata.Published, "an audio packet can still contain samples from the preceding private source")
	case <-time.After(time.Second):
		t.Fatal("tail was not queued for private delivery")
	}
	buffered := mm.BufferedSegments(did)
	require.False(t, buffered[len(buffered)-1].Published, "public clip selection must exclude a source after private audio is appended")

}

func audioAddendumSourceForTest(t *testing.T, ms *MediaSignerLocal) ([][]byte, []byte) {
	t.Helper()
	ctx := t.Context()
	sources := allSignedBareSegments(t, ctx, ms, getFixture("ertmp-red-h264.mp4"))
	cert, key := nodeSignerForTest(t)
	encoder := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
	var completed [][]byte
	var tail []byte
	tr := encoder.newStreamTranscoder(ctx, "opus", cert, key, func(_ any, data []byte, audioOnly bool) {
		if audioOnly {
			tail = data
		} else {
			completed = append(completed, data)
		}
	})
	t.Cleanup(func() { require.NoError(t, tr.Close()) })
	for _, source := range sources {
		require.NoError(t, tr.Feed(source, nil))
	}
	require.NoError(t, tr.Close())
	require.NotEmpty(t, tail)
	return completed, tail
}

func audioSignerForTest(t *testing.T) *MediaSignerLocal {
	t.Helper()
	ms := newBareSegmentSigner(t)
	ms.StreamerName = ms.DID()
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(ms.PrebuiltManifest, &manifest))
	assertions := manifest["assertions"].([]any)
	assertions[1].(map[string]any)["data"].(map[string]any)["dc:creator"] = ms.Streamer()
	assertions[0].(map[string]any)["data"].(map[string]any)["actions"] = []any{map[string]any{"action": "c2pa.created"}, map[string]any{"action": "c2pa.published"}}
	var err error
	ms.PrebuiltManifest, err = json.Marshal(manifest)
	require.NoError(t, err)
	return ms
}
