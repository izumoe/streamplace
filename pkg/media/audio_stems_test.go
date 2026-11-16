package media

import (
	"bytes"
	"context"
	"fmt"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
	"os"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/muxl"
	"testing"
	"time"
)

// audioCfg is a catalog audio rendition at track id with codec and group.
func audioCfg(id uint32, codec string, group uint32, label string) muxl.MuxlAudioConfig {
	return muxl.MuxlAudioConfig{
		Codec:            codec,
		Container:        muxl.MuxlContainer{Kind: "cmaf", Timescale: 48000, TrackID: id},
		SampleRate:       48000,
		NumberOfChannels: 2,
		AlternateGroup:   group,
		Label:            label,
	}
}

func catalogOf(audio ...muxl.MuxlAudioConfig) *muxl.MuxlCatalog {
	cat := &muxl.MuxlCatalog{
		Video: &muxl.MuxlCatalogVideo{Renditions: map[string]muxl.MuxlVideoConfig{
			"v": {Codec: "avc1.640028", Container: muxl.MuxlContainer{Kind: "cmaf", Timescale: 90000, TrackID: 1}},
		}},
		Audio: &muxl.MuxlCatalogAudio{Renditions: map[string]muxl.MuxlAudioConfig{}},
	}
	for i, a := range audio {
		cat.Audio.Renditions[fmt.Sprint("a", i)] = a
	}
	return cat
}

// The main audio is the lowest alternateGroup; an ungrouped segment is one
// group. Codec completion looks at the main group only: a stem missing a
// codec is distributed as sent, and a main group with both codecs needs
// nothing whatever the stems carry.
func TestMainAudioGroupSelectionAndCompletion(t *testing.T) {
	ungroupedAAC := catalogOf(audioCfg(2, "mp4a.40.2", 0, ""))
	ungroupedBoth := catalogOf(audioCfg(3, "opus", 0, ""), audioCfg(2, "mp4a.40.2", 0, ""))
	grouped := catalogOf(
		audioCfg(4, "mp4a.40.2", 2, "Music"), audioCfg(5, "opus", 2, "Music"),
		audioCfg(2, "mp4a.40.2", 1, "Mix"), audioCfg(3, "opus", 1, "Mix"),
		audioCfg(7, "opus", 3, "Mic"), audioCfg(8, "mp4a.40.2", 4, "Alerts"), audioCfg(9, "opus", 5, "Game"),
	)
	mainAACOnly := catalogOf(audioCfg(2, "mp4a.40.2", 1, "Mix"), audioCfg(4, "mp4a.40.2", 2, "Music"), audioCfg(5, "opus", 2, "Music"))
	stemAACOnly := catalogOf(audioCfg(2, "mp4a.40.2", 1, "Mix"), audioCfg(3, "opus", 1, "Mix"), audioCfg(4, "mp4a.40.2", 2, "Music"))

	ids := func(tracks []muxl.MuxlAudioConfig) []uint32 {
		var out []uint32
		for _, a := range tracks {
			out = append(out, a.TrackID())
		}
		return out
	}
	require.Equal(t, []uint32{2, 3}, ids(mainAudioTracks(audioTracks(ungroupedBoth))), "ungrouped: every track is main, in track order")
	require.Equal(t, []uint32{2, 3}, ids(mainAudioTracks(audioTracks(grouped))), "grouped: the lowest group is main")
	require.Equal(t, []uint32{5, 7}, ids(stemOpusTracks(audioTracks(grouped))), "stems: one Opus track per higher group, ascending, at most two")
	require.Empty(t, stemOpusTracks(audioTracks(ungroupedBoth)), "ungrouped: no stems")
	require.Empty(t, stemOpusTracks(audioTracks(stemAACOnly)), "a stem without Opus can't reach WebRTC")

	for _, tc := range []struct {
		name   string
		cat    *muxl.MuxlCatalog
		target string
		need   bool
	}{
		{"ungrouped AAC only", ungroupedAAC, "opus", true},
		{"ungrouped both", ungroupedBoth, "", false},
		{"grouped, every group complete", grouped, "", false},
		{"grouped, main lacks Opus", mainAACOnly, "opus", true},
		{"grouped, only a stem lacks Opus", stemAACOnly, "", false},
		{"no video", &muxl.MuxlCatalog{Audio: ungroupedAAC.Audio}, "", false},
		{"no catalog", nil, "", false},
	} {
		target, need := completionTarget(tc.cat)
		require.Equal(t, tc.need, need, tc.name)
		require.Equal(t, tc.target, target, tc.name)
	}
}

func groupedSignedSources(t *testing.T, ctx context.Context) [][]byte {
	t.Helper()
	input, err := os.ReadFile(getFixture("ertmp-red-h264.mp4"))
	require.NoError(t, err)
	main, err := muxl.RunMuxlCanonicalize(ctx, input, nil, muxl.WithTrackMeta(map[uint32]muxl.TrackMeta{2: {AlternateGroup: 1, Label: "Mix"}}))
	require.NoError(t, err)
	input, err = os.ReadFile(getFixture("h264-opus-frag.mp4"))
	require.NoError(t, err)
	stem, err := muxl.RunMuxlCanonicalize(ctx, input, map[uint32]uint32{2: 4}, muxl.WithTrackMeta(map[uint32]muxl.TrackMeta{4: {AlternateGroup: 2, Label: "Music: 音楽"}}))
	require.NoError(t, err)
	events, err := unwrapMuxlEvents(ctx, stem)
	require.NoError(t, err)
	combined := append(main, concatTrackAcrossSegments(events, "4")...)
	var presentation bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(combined), "fmp4", &presentation))
	signer := newBareSegmentSigner(t)
	signing, err := signer.SigningInput()
	require.NoError(t, err)
	signing.TrackManifest, err = signer.buildManifest(ctx, time.Now().UnixMilli())
	require.NoError(t, err)
	signing.WrapperManifest = signing.TrackManifest
	signing.SourceTrackMeta = true
	events, err = collectMuxlEvents(func(out chan *muxl.MuxlEvent) error {
		return muxl.RunMuxlSignSegment(ctx, bytes.NewReader(presentation.Bytes()), signing, nil, nil, out)
	})
	require.NoError(t, err)
	var source [][]byte
	for _, event := range events {
		if event.Type == "signed-segment" {
			source = append(source, concatTracksSorted(event.Tracks))
		}
	}
	require.NotEmpty(t, source)
	return source
}

func TestGroupedAudioValidationAndCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	source := groupedSignedSources(t, ctx)
	valid, err := ValidateMP4Media(ctx, source[0])
	require.NoError(t, err)
	require.Len(t, valid.MediaData.Audio, 2)
	require.Equal(t, uint32(1), valid.MediaData.Audio[0].AlternateGroup)
	require.Equal(t, "Mix", valid.MediaData.Audio[0].Label)
	require.Equal(t, uint32(2), valid.MediaData.Audio[1].AlternateGroup)
	require.Equal(t, "Music: 音楽", valid.MediaData.Audio[1].Label)
	target, need := audioCompletionTargetFromMedia(valid.MediaData)
	require.True(t, need, "a stem's Opus must not count as the main mix's codec")
	require.Equal(t, "opus", target)
	_, err = selectSegmentTracks(ctx, source[0], 0, "avc1", true)
	require.ErrorContains(t, err, "requested audio codec unavailable", "never substitute a stem for the missing full mix")
	cert, key := nodeSignerForTest(t)
	mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "node.test"}}
	var completed [][]byte
	tr := mm.newStreamTranscoder(ctx, target, cert, key, func(_ any, data []byte, _ bool) { completed = append(completed, data) })
	t.Cleanup(func() { require.NoError(t, tr.Close()) })
	for _, segment := range source {
		require.NoError(t, tr.Feed(segment, nil))
	}
	require.NoError(t, tr.Close())
	require.Greater(t, len(completed), len(source), "the codec tail remains independently delivered")
	for i, output := range completed {
		_, err := ValidateMP4MediaC2PA(ctx, output)
		require.NoError(t, err, "completed tracks and tail keep valid signatures")
		events, err := unwrapMuxlEvents(ctx, output)
		require.NoError(t, err)
		cat := catalogFromEvents(events)
		for _, audio := range audioTracks(cat) {
			if audio.TrackID() != 2 && audio.TrackID() != 4 {
				require.Equal(t, uint32(1), audio.AlternateGroup, "new codec stays in the main group, including EOS")
				require.Equal(t, "Mix", audio.Label)
			}
		}
		if i < len(source) {
			require.True(t, bytes.HasPrefix(output, source[i]), "original video/AAC/stem signatures and bytes are retained")
			packet, err := Packetize(ctx, mm.cli, &bus.Seg{Muxl: output}, 0)
			require.NoError(t, err)
			require.NotEmpty(t, packet.Audio)
			require.Len(t, packet.Stems, 1)
			require.Equal(t, uint32(2), packet.Stems[0].Group)
			require.NotEmpty(t, packet.Stems[0].Audio)
			if i == 0 {
				peer := NewOffline(mm.cli)
				peer.bus = bus.NewBus()
				valid, err := ValidateMP4MediaC2PA(ctx, output)
				require.NoError(t, err)
				peer.bufferSegment("stems", valid.Meta.StartTime.Time(), true, false, output, 0, *valid.Manifest.Label)
				peer.rememberSourceAudio("stems", *valid.Manifest.Label, events)
				peer.feedLiveWindow(ctx, "stems", output, valid.Meta.StartTime.Time(), true)
				addendum, err := peer.mintVideoRenditions(ctx, output, []RenditionInput{
					{MP4: renditionMP4(t, 160, 120)}, {MP4: renditionMP4(t, 320, 240)}, {MP4: renditionMP4(t, 640, 360)},
				}, cert, key)
				require.NoError(t, err)
				require.NoError(t, peer.PublishRenditionsForPlayback(ctx, "stems", addendum, true))
				for _, name := range []string{"120p", "240p", "360p"} {
					rendition := peer.bus.LatestSegment("stems", name)
					require.NotNil(t, rendition)
					require.NotEmpty(t, rendition.PacketizedData.Video)
					require.NotEmpty(t, rendition.PacketizedData.Audio)
					require.Equal(t, packet.Stems, rendition.PacketizedData.Stems, "all video renditions retain their admitted source's stems")
				}
			}
		}
	}
}

func TestWebRTCPlaybackDeliversMainAndStemTrackIDs(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	mm := loopbackPlaybackManager()
	packet := playbackPacketFixture(t)
	packet.Stems = []bus.PacketizedStem{{Group: 7, Audio: packet.Audio}, {Group: 3, Audio: packet.Audio}}
	mm.bus.PublishSegment(ctx, t.Name(), WebRTCSourceRendition, &bus.Seg{Published: true, PacketizedData: packet})
	client, err := mm.webrtcAPI.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	defer client.Close()
	for range 3 {
		_, err := client.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
		require.NoError(t, err)
	}
	received := make(chan string, 3)
	client.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for range 10 {
			if _, _, err := track.ReadRTP(); err != nil {
				return
			}
		}
		received <- track.ID()
	})
	offer, err := client.CreateOffer(nil)
	require.NoError(t, err)
	require.NoError(t, client.SetLocalDescription(offer))
	select {
	case <-webrtc.GatheringCompletePromise(client):
	case <-ctx.Done():
		t.Fatal("ICE gather timed out")
	}
	answer, err := mm.WebRTCPlayback2(ctx, t.Name(), "audio", client.LocalDescription(), "")
	require.NoError(t, err)
	require.NoError(t, client.SetRemoteDescription(*answer))
	var ids []string
	for len(ids) < 3 {
		select {
		case id := <-received:
			ids = append(ids, id)
		case <-ctx.Done():
			t.Fatalf("audio delivery timed out; got tracks %v", ids)
		}
	}
	require.ElementsMatch(t, []string{"audio", "audio-g3", "audio-g7"}, ids)
}
