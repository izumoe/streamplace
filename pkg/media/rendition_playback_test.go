package media

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/crypto/signers"
)

// A node that receives a stream's rendition addendum (a syndicating node)
// packetizes each rendition with the source segment's Opus audio and
// publishes it on its bus under the rendition's name, so WebRTC viewers
// there can pick it.
func TestPublishRenditionsForPlayback(t *testing.T) {
	ctx := context.Background()
	src := sourceSegmentForTest(t)
	r160 := renditionMP4(t, 160, 120)
	cert, keyPEM := nodeSignerForTest(t)
	b := bus.NewBus()
	mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "node.test"}, liveWindows: map[string]*liveWindowState{}, bus: b}
	addendum, err := mm.mintVideoRenditions(ctx, src, []RenditionInput{{Name: "120p", MP4: r160}}, cert, keyPEM)
	require.NoError(t, err)

	const did = "did:test:streamer"
	sub := b.SubscribeSegment(ctx, did, "120p")
	defer b.UnsubscribeSegment(ctx, did, "120p", sub)
	compatible := b.SubscribeSegment(ctx, did, WebRTCSourceRendition)
	defer b.UnsubscribeSegment(ctx, did, WebRTCSourceRendition, compatible)

	// Without the source segment's audio there is nothing to pair with.
	mm.PublishRenditionsForPlayback(ctx, did, addendum, true)
	require.Empty(t, sub.C)

	mm.rememberSourceAudio(ctx, did, src)
	want, err := Packetize(ctx, mm.cli, &bus.Seg{Data: r160}, 0)
	require.NoError(t, err)
	mm.PublishRenditionsForPlayback(ctx, did, addendum, true)
	select {
	case seg := <-sub.C:
		require.True(t, seg.Published)
		require.NotNil(t, seg.PacketizedData)
		require.NotEmpty(t, seg.PacketizedData.Video, "video samples")
		require.Len(t, seg.PacketizedData.Video, len(want.Video), "every rendition fragment reaches peer playback")
		require.NotEmpty(t, seg.PacketizedData.Audio, "audio samples")
		require.Greater(t, seg.PacketizedData.Duration, time.Duration(0))
	case <-time.After(10 * time.Second):
		t.Fatal("no rendition segment published")
	}
	require.Empty(t, compatible.C, "an H264 publisher already provides its default playback source")
}

func TestReplicatedAV1RenditionProvidesWebRTCSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	ms := newBareSegmentSigner(t)
	sources := allSignedBareSegments(t, ctx, ms, getFixture("av1-aac-frag.mp4"))
	key, err := signers.MarshalES256KPrivateKeyPEM(ms.Signer)
	require.NoError(t, err)
	b := bus.NewBus()
	mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "node.test"}, bus: b}
	var completed [][]byte
	encoder := mm.newStreamTranscoder(ctx, "opus", ms.Cert, key, func(_ any, segment []byte) { completed = append(completed, segment) })
	for _, source := range sources {
		require.NoError(t, encoder.Feed(source, nil))
	}
	require.NoError(t, encoder.Close())
	require.Len(t, completed, len(sources))
	cert, nodeKey := nodeSignerForTest(t)
	require.GreaterOrEqual(t, len(completed), 2)
	addendum, err := mm.mintVideoRenditions(ctx, completed[1], []RenditionInput{{Name: "120p", MP4: renditionMP4(t, 160, 120)}}, cert, nodeKey)
	require.NoError(t, err)
	const did = "did:example:replicated-av1"
	compatible := b.SubscribeSegment(ctx, did, WebRTCSourceRendition)
	defer b.UnsubscribeSegment(ctx, did, WebRTCSourceRendition, compatible)
	named := b.SubscribeSegment(ctx, did, "120p")
	defer b.UnsubscribeSegment(ctx, did, "120p", named)
	mm.rememberSourceAudio(ctx, did, completed[1])
	mm.PublishRenditionsForPlayback(ctx, did, addendum, true)
	require.Len(t, named.C, 1, "nonzero decode times pair correctly across source and rendition timescales")
	require.Len(t, compatible.C, 1, "a peer uses the H264 addendum for default WHEP when its source is AV1")
	segment := <-compatible.C
	require.True(t, segment.Published)
	require.NotEmpty(t, segment.PacketizedData.Video)
	require.NotEmpty(t, segment.PacketizedData.Audio)
}
