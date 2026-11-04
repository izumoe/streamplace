package media

import (
	"bytes"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/muxl"
)

func TestAudioEOSEmitsFetchableHLSTailWithoutFinalizingLiveStream(t *testing.T) {
	ctx := t.Context()
	signer := audioSignerForTest(t)
	completed, tail := audioAddendumSourceForTest(t, signer)
	mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
	mm.bus = bus.NewBus()
	did := signer.Streamer()
	var source string
	var start time.Time
	for _, segment := range completed {
		valid, err := ValidateMP4MediaC2PA(ctx, segment)
		require.NoError(t, err)
		require.NotNil(t, valid.Manifest.Label)
		source, start = *valid.Manifest.Label, valid.Meta.StartTime.Time()
		mm.feedLiveWindow(ctx, did, segment, start, valid.Meta.Published)
		mm.bufferSegment(did, start, valid.Meta.Published, true, segment, 0, source)
		events, err := unwrapMuxlEvents(ctx, segment)
		require.NoError(t, err)
		mm.rememberSourceAudio(did, source, events)
	}
	events, err := unwrapMuxlEvents(ctx, tail)
	require.NoError(t, err)
	cat := catalogFromEvents(events)
	require.NotNil(t, cat.Audio)
	var trackID string
	var duration time.Duration
	for _, audio := range cat.Audio.Renditions {
		trackID = strconv.FormatUint(uint64(audio.TrackID()), 10)
		for _, event := range events {
			duration += time.Duration(event.Durations[trackID]) * time.Second / time.Duration(audio.Timescale())
		}
	}
	require.Positive(t, duration)
	require.Less(t, duration, liveWindowMinFragment, "this regression requires a real EOS tail below the HLS fragment target")
	require.NoError(t, mm.PublishAudioAddendum(ctx, did, tail, source, start, true))
	window, published := mm.GetLiveWindow(did)
	require.NotNil(t, window)
	require.True(t, published)
	track := window.Track(trackID)
	require.NotNil(t, track)
	found := false
	for _, segment := range track.Segments {
		data := window.SegmentData(trackID, segment.Seq)
		if !bytes.Contains(data, tail) {
			continue
		}
		found = true
		verification, err := muxl.RunMuxlVerify(ctx, bytes.NewReader(data))
		require.NoError(t, err)
		_, err = parseVerifiedSegments([]byte(verification))
		require.NoError(t, err, "HLS serves the exact valid signed audio assets")
		playlist := window.MediaPlaylist(trackID, "init", func(sequence uint64) string { return fmt.Sprintf("segment-%d.m4s", sequence) })
		require.Contains(t, playlist, fmt.Sprintf("segment-%d.m4s", segment.Seq))
		require.NotContains(t, playlist, "#EXT-X-ENDLIST")
		require.NotContains(t, playlist, "#EXT-X-PLAYLIST-TYPE:VOD")
	}
	require.True(t, found, "the final signed audio packets must be advertised and fetchable without a future GoP")
}
