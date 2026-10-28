package media

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/muxl"
	"stream.place/streamplace/pkg/renditions"
	"stream.place/streamplace/pkg/transcoder"
)

func TestTranscoderServicePublishesSignedRenditions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	source := sourceSegmentForTest(t)
	presentation, err := PresentationWithOpus(ctx, source)
	require.NoError(t, err)
	handler, err := transcoder.NewHandler(1)
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	profiles := []renditions.Profile{
		{Name: "120p", Width: 160, Height: 120, Bitrate: 200_000, Profile: "high"},
		{Name: "60p", Width: 80, Height: 60, Bitrate: 100_000, Profile: "baseline"},
	}
	outputs, err := transcoder.Transcode(ctx, server.URL+"/transcode", presentation, profiles)
	require.NoError(t, err)
	require.Len(t, outputs, len(profiles))
	inputs := make([]RenditionInput, len(outputs))
	for i, output := range outputs {
		inputs[i] = RenditionInput{Name: profiles[i].Name, MP4: output}
	}
	cert, key := nodeSignerForTest(t)
	manager := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
	addendum, err := manager.MintVideoRenditionsWith(ctx, source, inputs, cert, key)
	require.NoError(t, err)
	require.NotEmpty(t, addendum)
	_, err = muxl.RunMuxlVerify(ctx, bytes.NewReader(addendum))
	require.NoError(t, err, "each native rendition carries a valid node signature")
	completed := append(bytes.Clone(source), addendum...)
	_, err = ValidateMP4Media(ctx, completed)
	require.NoError(t, err, "rendition provenance binds to the original signed source")

	const did = "did:test:streamer"
	started := time.Now()
	manager.feedLiveWindow(ctx, did, source, started, true)
	manager.FeedLiveRenditions(ctx, did, addendum, started, true)
	window := manager.GetLiveWindow(did)
	require.NotNil(t, window)
	master := window.MasterPlaylist(func(id string) string { return id + ".m3u8" })
	require.Equal(t, len(profiles)+1, strings.Count(master, "#EXT-X-STREAM-INF"), "source and every requested rendition are published")
	sourceTrack := window.Track("1")
	require.NotNil(t, sourceTrack)
	require.Len(t, sourceTrack.Segments, 1)
	for i, profile := range profiles {
		id := fmt.Sprint(RenditionTrackID(i))
		track := window.Track(id)
		require.NotNil(t, track, "requested rendition %s retains its ladder track id", profile.Name)
		require.Equal(t, uint32(profile.Width), track.Width)
		require.Equal(t, uint32(profile.Height), track.Height)
		require.Len(t, track.Segments, 1)
		require.Equal(t, sourceTrack.Segments[0].SampleCount, track.Segments[0].SampleCount, "no source frames disappear before signing/publication")
		require.InDelta(t,
			float64(sourceTrack.Segments[0].DurationTicks)/float64(sourceTrack.Timescale),
			float64(track.Segments[0].DurationTicks)/float64(track.Timescale),
			0.001, "rendition duration stays aligned with source")
		data := window.SegmentData(id, track.Segments[0].Seq)
		require.NotEmpty(t, data)
		_, err := muxl.RunMuxlVerify(ctx, bytes.NewReader(data))
		require.NoError(t, err, "the live window serves the signed bytes intact")
		require.NotEmpty(t, window.InitSegment(id))
	}
}
