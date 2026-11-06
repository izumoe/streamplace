package media

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/muxl"
)

// Independent rendition tracks can overtake the canonical source. A receiver
// must leave them retryable, with no premature HLS or playback publication.
func TestReceiveVideoRenditionWaitsForSourceAdmission(t *testing.T) {
	ctx := t.Context()
	ms := audioSignerForTest(t)
	source := allSignedBareSegments(t, ctx, ms, getFixture("h264-opus-frag.mp4"))[0]
	valid, err := ValidateMP4MediaC2PA(ctx, source)
	require.NoError(t, err)
	events, err := unwrapMuxlEvents(ctx, source)
	require.NoError(t, err)
	var flat bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(source), "flat", &flat))
	cert, key := nodeSignerForTest(t)
	mint := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
	addendum, err := mint.mintVideoRenditions(ctx, source, []RenditionInput{{MP4: flat.Bytes()}}, cert, key)
	require.NoError(t, err)
	want := canonicalAudioPackets(t, ctx, source, "opus")
	for _, cached := range []bool{false, true} {
		for _, published := range []bool{false, true} {
			t.Run(fmt.Sprintf("audio-cached=%t/public=%t", cached, published), func(t *testing.T) {
				mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
				mm.bus = bus.NewBus()
				did := ms.Streamer()
				video, ok := primaryVideoTrack(catalogFromEvents(events), 0, "")
				require.True(t, ok)
				name := RenditionName(video.CodedWidth, video.CodedHeight)
				sub := mm.bus.SubscribeSegment(ctx, did, name)
				t.Cleanup(func() { mm.bus.UnsubscribeSegment(ctx, did, name, sub) })
				if cached {
					mm.rememberSourceAudio(did, *valid.Manifest.Label, events)
				}
				require.ErrorIs(t, mm.ReceiveRenditions(ctx, did, addendum), ErrAudioSourceMissing,
					"the transport must retain a valid video rendition until its source is admitted")
				require.Empty(t, sub.C)
				window, _ := mm.GetLiveWindow(did)
				require.Nil(t, window, "a rejected attempt cannot create a private video-only HLS window")

				mm.bufferSegment(did, valid.Meta.StartTime.Time(), published, false, source, 0, *valid.Manifest.Label)
				mm.rememberSourceAudio(did, *valid.Manifest.Label, events)
				mm.feedLiveWindow(ctx, did, source, valid.Meta.StartTime.Time(), published)
				require.NoError(t, mm.ReceiveRenditions(ctx, did, addendum), "retry succeeds after the source arrives")
				require.Len(t, sub.C, 1, "only the successful retry publishes video")
				segment := <-sub.C
				require.Equal(t, published, segment.Published)
				require.NotEmpty(t, segment.PacketizedData.Video)
				require.Len(t, segment.PacketizedData.Audio, len(want))
				for i := range want {
					require.Equal(t, want[i].data, segment.PacketizedData.Audio[i].Data)
				}
				window, public := mm.GetLiveWindow(did)
				require.NotNil(t, window)
				require.Equal(t, published, public)
				require.ElementsMatch(t, []string{name}, mm.LiveRenditionNames(did), "the retried video remains available through HLS")
			})
		}
	}
}

func TestAdmittedVideoOnlySourceKeepsHLSRenditions(t *testing.T) {
	ctx := t.Context()
	ms := audioSignerForTest(t)
	complete := allSignedBareSegments(t, ctx, ms, getFixture("h264-opus-frag.mp4"))[0]
	events, err := unwrapMuxlEvents(ctx, complete)
	require.NoError(t, err)
	video, ok := primaryVideoTrack(catalogFromEvents(events), 0, "")
	require.True(t, ok)
	// Canonical tracks are independently signed. Keeping just the original
	// video preserves its source signature without creating an audio track.
	source := concatTrackAcrossSegments(events, fmt.Sprint(video.TrackID()))
	valid, err := ValidateMP4MediaC2PA(ctx, source)
	require.NoError(t, err)
	var flat bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(source), "flat", &flat))
	cert, key := nodeSignerForTest(t)
	did := ms.Streamer()
	mm := NewOffline(&config.CLI{BroadcasterHost: "node.test", Syndicate: []string{did}})
	mm.bus = bus.NewBus()
	name := RenditionName(video.CodedWidth, video.CodedHeight)
	sub := mm.bus.SubscribeSegment(ctx, did, name)
	t.Cleanup(func() { mm.bus.UnsubscribeSegment(ctx, did, name, sub) })
	require.NoError(t, mm.distributeSegment(ctx, &validatedSegment{
		repoDID: did, label: *valid.Manifest.Label, meta: valid.Meta,
	}, source, flat.Bytes()), "distribution retains the admitted source identity even without Opus")
	addendum, err := mm.mintVideoRenditions(ctx, source, []RenditionInput{{MP4: flat.Bytes()}}, cert, key)
	require.NoError(t, err)
	require.NoError(t, mm.ReceiveRenditions(ctx, did, addendum))
	window, public := mm.GetLiveWindow(did)
	require.NotNil(t, window)
	require.True(t, public)
	require.ElementsMatch(t, []string{name}, mm.LiveRenditionNames(did))
	require.Empty(t, window.PrimaryAudioTrackID(), "HLS must not invent audio for a video-only source")
	require.Empty(t, sub.C, "the Opus-dependent WebRTC path can stay absent while HLS keeps the video")
}
