package media

import (
	"bytes"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
)

func TestPublicRenditionKeepsAdmittedAudioAcrossPrivateReconnect(t *testing.T) {
	ctx := t.Context()
	signer := audioSignerForTest(t)
	source := allSignedBareSegments(t, ctx, signer, getFixture("h264-opus-frag.mp4"))[0]
	reconnected, _ := audioAddendumSourceForTest(t, signer)
	privateSource := reconnected[0]
	want := canonicalAudioPackets(t, ctx, source, "opus")
	privateAudio := canonicalAudioPackets(t, ctx, privateSource, "opus")
	require.NotEmpty(t, want)
	require.NotEmpty(t, privateAudio)
	require.False(t, bytes.Equal(want[0].data, privateAudio[0].data), "the fixtures must contain different encoded audio")
	valid, err := ValidateMP4MediaC2PA(ctx, source)
	require.NoError(t, err)
	privateValid, err := ValidateMP4MediaC2PA(ctx, privateSource)
	require.NoError(t, err)
	events, err := unwrapMuxlEvents(ctx, source)
	require.NoError(t, err)
	privateEvents, err := unwrapMuxlEvents(ctx, privateSource)
	require.NoError(t, err)
	video, ok := primaryVideoTrack(catalogFromEvents(events), 0, "")
	require.True(t, ok)
	tfdt, ok := firstTfdt(concatTrackAcrossSegments(events, strconv.FormatUint(uint64(video.TrackID()), 10)))
	require.True(t, ok)

	mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
	mm.bus = bus.NewBus()
	did := signer.Streamer()
	mm.bufferSegment(did, valid.Meta.StartTime.Time(), true, false, source, 0, *valid.Manifest.Label)
	mm.rememberSourceAudio(did, *valid.Manifest.Label, events)
	mm.feedLiveWindow(ctx, did, source, valid.Meta.StartTime.Time(), true)
	cert, key := nodeSignerForTest(t)
	addendum, err := mm.mintVideoRenditions(ctx, source, []RenditionInput{{MP4: renditionMP4(t, 160, 120)}}, cert, key)
	require.NoError(t, err)
	sub := mm.bus.SubscribeSegment(ctx, did, "120p")
	t.Cleanup(func() { mm.bus.UnsubscribeSegment(ctx, did, "120p", sub) })
	window, _ := mm.GetLiveWindow(did)
	require.NotNil(t, window)
	entered, release, playlistDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(playlistDone)
		first := true
		window.MasterPlaylist(func(id string) string {
			if first {
				first = false
				close(entered)
				<-release
			}
			return id + ".m3u8"
		})
	}()
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() {
		unblock()
		waitLiveWindowWork(t, playlistDone)
	})
	waitLiveWindowWork(t, entered)
	published := make(chan error, 1)
	go func() { published <- mm.PublishRenditionsForPlayback(ctx, did, addendum, true) }()
	waitLiveWindowStack(t, "(*MediaManager).PublishRenditionsForPlayback", "(*Writer).Observe")

	// A reconnect can replace a cached clock while the public publication is
	// waiting on its HLS writer. Its private audio cannot enter that publication.
	mm.bufferSegment(did, privateValid.Meta.StartTime.Time(), false, false, privateSource, 0, *privateValid.Manifest.Label)
	mm.rememberSourceAudio(did, *privateValid.Manifest.Label, privateEvents)
	replacement, found := mm.sourceAudioAt(did, tfdt, video.Timescale())
	require.True(t, found)
	require.Equal(t, *privateValid.Manifest.Label, replacement.sourceLabel, "the reconnect must replace the exact public media clock")
	unblock()
	waitLiveWindowWork(t, playlistDone)
	select {
	case err := <-published:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("rendition publication did not finish")
	}
	select {
	case segment := <-sub.C:
		require.True(t, segment.Published)
		require.Len(t, segment.PacketizedData.Audio, len(want))
		for i := range want {
			require.True(t, bytes.Equal(want[i].data, segment.PacketizedData.Audio[i].Data), "public packet %d must use the admitted source audio", i)
		}
		require.Equal(t, *valid.Manifest.Label, segment.Filepath)
	case <-time.After(time.Second):
		t.Fatal("public rendition was not published")
	}
}

func TestRenditionRejectsTracksFromDifferentSourceAdmissions(t *testing.T) {
	ctx := t.Context()
	signer := audioSignerForTest(t)
	sources := allSignedBareSegments(t, ctx, signer, getFixture("h264-opus-frag.mp4"))
	require.GreaterOrEqual(t, len(sources), 2)
	mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
	mm.bus = bus.NewBus()
	did := signer.Streamer()
	cert, key := nodeSignerForTest(t)
	inputs := []RenditionInput{{MP4: renditionMP4(t, 160, 120)}, {MP4: renditionMP4(t, 320, 240)}}
	var mixed []byte
	for i, source := range sources[:2] {
		valid, err := ValidateMP4MediaC2PA(ctx, source)
		require.NoError(t, err)
		events, err := unwrapMuxlEvents(ctx, source)
		require.NoError(t, err)
		mm.bufferSegment(did, valid.Meta.StartTime.Time(), i == 0, false, source, 0, *valid.Manifest.Label)
		mm.rememberSourceAudio(did, *valid.Manifest.Label, events)
		addendum, err := mm.mintVideoRenditions(ctx, source, inputs, cert, key)
		require.NoError(t, err)
		renditions, err := unwrapMuxlEvents(ctx, addendum)
		require.NoError(t, err)
		for _, video := range catalogFromEvents(renditions).Video.Renditions {
			if video.CodedHeight == uint32(120*(i+1)) {
				mixed = append(mixed, concatTrackAcrossSegments(renditions, strconv.FormatUint(uint64(video.TrackID()), 10))...)
			}
		}
	}
	_, tail, err := verifiedRenditions(ctx, mixed)
	require.NoError(t, err, "each rendition is independently signed and valid")
	require.False(t, tail)
	require.ErrorContains(t, mm.PublishRenditionsForPlayback(ctx, did, mixed, true), "crosses source admissions")
	window, _ := mm.GetLiveWindow(did)
	require.Nil(t, window, "a mixed-source addendum cannot publish HLS under one source's policy")
}

func TestLateRenditionCannotReversePublicationTransition(t *testing.T) {
	ctx := t.Context()
	signer := audioSignerForTest(t)
	sources := allSignedBareSegments(t, ctx, signer, getFixture("h264-opus-frag.mp4"))
	require.GreaterOrEqual(t, len(sources), 2)
	cert, key := nodeSignerForTest(t)
	encoder := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
	addendum, err := encoder.mintVideoRenditions(ctx, sources[0], []RenditionInput{{MP4: renditionMP4(t, 160, 120)}}, cert, key)
	require.NoError(t, err)
	for _, firstPublic := range []bool{false, true} {
		name := "private to public"
		if firstPublic {
			name = "public to private"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
			mm.bus = bus.NewBus()
			did := signer.Streamer()
			var previous time.Time
			for i, source := range sources[:2] {
				valid, err := ValidateMP4MediaC2PA(ctx, source)
				require.NoError(t, err)
				start := valid.Meta.StartTime.Time()
				if i > 0 {
					require.True(t, start.After(previous), "the second source must advance the publication timeline")
				}
				previous = start
				events, err := unwrapMuxlEvents(ctx, source)
				require.NoError(t, err)
				public := firstPublic == (i == 0)
				mm.bufferSegment(did, start, public, false, source, 0, *valid.Manifest.Label)
				mm.rememberSourceAudio(did, *valid.Manifest.Label, events)
				mm.feedLiveWindow(ctx, did, source, start, public)
			}
			current, public := mm.GetLiveWindow(did)
			require.NotNil(t, current)
			require.Equal(t, !firstPublic, public)
			before := current.TrackIDs()
			sub := mm.bus.SubscribeSegment(ctx, did, "120p")
			t.Cleanup(func() { mm.bus.UnsubscribeSegment(ctx, did, "120p", sub) })
			require.NoError(t, mm.PublishRenditionsForPlayback(ctx, did, addendum, firstPublic))
			after, public := mm.GetLiveWindow(did)
			require.Same(t, current, after, "a late rendition cannot replace the current publication's writer")
			require.Equal(t, !firstPublic, public)
			require.Equal(t, before, current.TrackIDs(), "the current writer cannot receive a rendition from the earlier publication")
			require.Empty(t, sub.C, "the obsolete publication cannot reach playback")
		})
	}
}
