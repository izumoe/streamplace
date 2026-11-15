package media

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
)

func TestReplicatedRenditionRequiresSourceIngredient(t *testing.T) {
	input := renditionMP4(t, 160, 120)
	for _, reconnect := range []bool{false, true} {
		name := "another streamer"
		if reconnect {
			name = "same streamer after reconnect"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			oldSigner, currentSigner := audioSignerForTest(t), audioSignerForTest(t)
			if reconnect {
				currentSigner = oldSigner
			}
			oldSource := allSignedBareSegments(t, ctx, oldSigner, getFixture("h264-opus-frag.mp4"))[0]
			currentSource := allSignedBareSegments(t, ctx, currentSigner, getFixture("h264-opus-frag.mp4"))[0]
			valid, err := ValidateMP4MediaC2PA(ctx, currentSource)
			require.NoError(t, err)
			previous, err := ValidateMP4MediaC2PA(ctx, oldSource)
			require.NoError(t, err)
			require.NotEqual(t, *previous.Manifest.Label, *valid.Manifest.Label)
			events, err := unwrapMuxlEvents(ctx, currentSource)
			require.NoError(t, err)
			did := currentSigner.Streamer()
			mm := NewOffline(&config.CLI{BroadcasterHost: "node.test", Syndicate: []string{did}})
			mm.bus = bus.NewBus()
			mm.nodeCert, mm.nodeKeyPEM = nodeSignerForTest(t)
			mm.nodeSignerOnce.Do(func() {})
			mm.bufferSegment(did, valid.Meta.StartTime.Time(), true, false, currentSource, 0, *valid.Manifest.Label)
			mm.rememberSourceAudio(did, *valid.Manifest.Label, events)
			mm.feedLiveWindow(ctx, did, currentSource, valid.Meta.StartTime.Time(), true)
			before := mm.LiveRenditionNames(did)
			data, err := mm.mintVideoRenditions(ctx, oldSource, []RenditionInput{{MP4: input}}, mm.nodeCert, mm.nodeKeyPEM)
			require.NoError(t, err)
			sub := mm.bus.SubscribeSegment(ctx, did, "120p")
			t.Cleanup(func() { mm.bus.UnsubscribeSegment(ctx, did, "120p", sub) })
			require.ErrorContains(t, mm.ReceiveRenditions(ctx, did, data), "source ingredient")
			require.Empty(t, sub.C)
			require.Equal(t, before, mm.LiveRenditionNames(did))
			_, _, err = mm.validateSource(ctx, append(bytes.Clone(currentSource), data...), false, did)
			require.ErrorContains(t, err, "source ingredient")
			video, ok := primaryVideoTrack(catalogFromEvents(events), 0, "")
			require.True(t, ok)
			original := concatTrackAcrossSegments(events, strconv.FormatUint(uint64(video.TrackID()), 10))
			require.NoError(t, mm.ReceiveRenditions(ctx, did, original), "an unchanged admitted source track needs no transcode ingredient")
			lowerSource := allSignedBareSegments(t, ctx, currentSigner, getFixture("ertmp-red-h264.mp4"))[0]
			lowerEvents, err := unwrapMuxlEvents(ctx, lowerSource)
			require.NoError(t, err)
			lowerVideo, ok := primaryVideoTrack(catalogFromEvents(lowerEvents), 0, "")
			require.True(t, ok)
			lower := concatTrackAcrossSegments(lowerEvents, strconv.FormatUint(uint64(lowerVideo.TrackID()), 10))
			require.NoError(t, mm.ReceiveRenditions(ctx, did, lower), "a separately carried original rung is authenticated by the streamer's source certificate")
			currentSigner.PrebuiltManifest = bytes.ReplaceAll(currentSigner.PrebuiltManifest, []byte("c2pa.published"), []byte("c2pa.created"))
			privateSource := allSignedBareSegments(t, ctx, currentSigner, getFixture("ertmp-red-h264.mp4"))[0]
			privateEvents, err := unwrapMuxlEvents(ctx, privateSource)
			require.NoError(t, err)
			privateVideo := concatTrackAcrossSegments(privateEvents, strconv.FormatUint(uint64(lowerVideo.TrackID()), 10))
			require.NoError(t, mm.ReceiveRenditions(ctx, did, privateVideo))
			_, public := mm.GetLiveWindow(did)
			require.False(t, public, "a private original rung cannot borrow its public primary's visibility")
			correct, err := mm.mintVideoRenditions(ctx, currentSource, []RenditionInput{{MP4: input}}, mm.nodeCert, mm.nodeKeyPEM)
			require.NoError(t, err)
			require.NoError(t, mm.ReceiveRenditions(ctx, did, correct))
			require.NotEmpty(t, sub.C)
		})
	}
}

func TestOriginalRenditionHonorsSignedExpiry(t *testing.T) {
	ctx := t.Context()
	signer := audioSignerForTest(t)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(signer.PrebuiltManifest, &manifest))
	manifest["assertions"] = append(manifest["assertions"].([]any), map[string]any{
		"label": "place.stream.metadata.configuration",
		"data":  map[string]any{"distributionPolicy": map[string]any{"deleteAfter": 1}},
	})
	var err error
	signer.PrebuiltManifest, err = json.Marshal(manifest)
	require.NoError(t, err)
	source := allSignedBareSegments(t, ctx, signer, getFixture("h264-opus-frag.mp4"))[0]
	valid, err := ValidateMP4MediaC2PA(ctx, source)
	require.NoError(t, err)
	events, err := unwrapMuxlEvents(ctx, source)
	require.NoError(t, err)
	lower := allSignedBareSegments(t, ctx, signer, getFixture("ertmp-red-h264.mp4"))[0]
	lowerEvents, err := unwrapMuxlEvents(ctx, lower)
	require.NoError(t, err)
	video, ok := primaryVideoTrack(catalogFromEvents(lowerEvents), 0, "")
	require.True(t, ok)
	original := concatTrackAcrossSegments(lowerEvents, strconv.FormatUint(uint64(video.TrackID()), 10))
	lowerValid, err := ValidateMP4MediaC2PA(ctx, original)
	require.NoError(t, err)
	require.NotNil(t, lowerValid.Meta.DistributionPolicy.DeleteAfterSeconds)
	expires := lowerValid.Meta.StartTime.Time().Add(time.Second)
	if wait := time.Until(expires); wait >= 0 {
		require.Less(t, wait, 3*time.Second)
		time.Sleep(wait + time.Millisecond)
	}
	for _, enabled := range []bool{false, true} {
		filters := &config.ContentFilters{}
		filters.DistributionPolicy.Enabled = enabled
		mm := NewOffline(&config.CLI{BroadcasterHost: "node.test", ContentFilters: filters})
		did := signer.Streamer()
		mm.bufferSegment(did, valid.Meta.StartTime.Time(), true, false, source, 0, *valid.Manifest.Label)
		mm.rememberSourceAudio(did, *valid.Manifest.Label, events)
		mm.feedLiveWindow(ctx, did, source, valid.Meta.StartTime.Time(), true)
		err := mm.ReceiveRenditions(ctx, did, original)
		if enabled {
			require.ErrorContains(t, err, "distribution policy expired")
		} else {
			require.NoError(t, err, "the operator's disabled expiry filter remains disabled")
		}
	}
}
