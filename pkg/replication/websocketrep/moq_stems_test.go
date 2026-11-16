package websocketrep

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/require"
	official "moq.dev/moq"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/moq"
	"stream.place/streamplace/pkg/muxl"
)

func signedGroupedMoqSegments(t *testing.T, ctx context.Context, metadata map[uint32]muxl.TrackMeta) [][]byte {
	t.Helper()
	input, err := os.ReadFile("../../../test/fixtures/h264-opus-frag.mp4")
	require.NoError(t, err)
	main, err := muxl.RunMuxlCanonicalize(ctx, input, nil)
	require.NoError(t, err)
	extra, err := muxl.RunMuxlCanonicalize(ctx, input, map[uint32]uint32{2: 4})
	require.NoError(t, err)
	tracks, err := media.MuxlTracks(ctx, extra)
	require.NoError(t, err)
	var presentation bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(append(main, tracks["audio4"]...)), "fmp4", &presentation))
	signer, err := media.NewEphemeralMediaSigner("stem-streamer")
	require.NoError(t, err)
	signing, err := signer.SigningInput()
	require.NoError(t, err)
	signing.TrackMeta = metadata
	signing.TrackManifest = []byte(`{"title":"stem fixture","assertions":[{"label":"cawg.metadata","data":{"@context":{"dc":"http://purl.org/dc/elements/1.1/"},"dc:creator":"stem-streamer","dc:title":"fixture","dc:date":"2026-11-15T12:00:00.000Z"}}]}`)
	signing.WrapperManifest = signing.TrackManifest
	events := make(chan *muxl.MuxlEvent, 16)
	done := make(chan error, 1)
	go func() {
		done <- muxl.RunMuxlSignSegment(ctx, bytes.NewReader(presentation.Bytes()), signing, nil, nil, events)
		close(events)
	}()
	var segments [][]byte
	for event := range events {
		if event.Type == "signed-segment" {
			segments = append(segments, append(append(bytes.Clone(event.Tracks["1"]), event.Tracks["2"]...), event.Tracks["4"]...))
		}
	}
	require.NoError(t, <-done)
	require.GreaterOrEqual(t, len(segments), 2)
	return segments
}

func moqStemPlaybackClient(t *testing.T, ctx context.Context, b *bus.Bus) *official.Client {
	t.Helper()
	serverCtx, cancel := context.WithCancel(ctx)
	address, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := address.LocalAddr().String()
	require.NoError(t, address.Close())
	cert, err := moq.NewSelfSignedCert()
	require.NoError(t, err)
	tlsFiles, err := moq.ManagedTLSFiles(t.TempDir(), cert.Certificate)
	require.NoError(t, err)
	r := &WebsocketReplicator{bus: b, cli: &config.CLI{MoqAddr: addr}, MoqTLS: tlsFiles}
	stopped := make(chan error, 1)
	go func() { stopped <- r.serveMoQ(serverCtx) }()
	t.Cleanup(func() { cancel(); require.ErrorIs(t, <-stopped, context.Canceled) })
	var client *official.Client
	require.Eventually(t, func() bool {
		client, err = r.dialMoq(serverCtx, fmt.Sprintf("moqt://%s?certhash=%s", addr, cert.Hash()))
		return err == nil
	}, 5*time.Second, 20*time.Millisecond)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestMoqPlaybackPublishesOptionalSignedStemBroadcast(t *testing.T) {
	for _, ending := range []string{"private source", "parent session end"} {
		t.Run(ending, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			segments := signedGroupedMoqSegments(t, ctx, map[uint32]muxl.TrackMeta{2: {AlternateGroup: 1, Label: "Mix"}, 4: {AlternateGroup: 2, Label: "Music: 音楽"}})
			b := bus.NewBus()
			const streamer = "did:plc:stems"
			b.PublishSegment(ctx, streamer, media.PlaybackSourceRendition, &bus.Seg{Filepath: "source", Muxl: segments[0], Published: true, BatchDuration: time.Second})
			client := moqStemPlaybackClient(t, ctx, b)
			parent, err := requestMoqBroadcast(ctx, client, "playback/"+streamer)
			require.NoError(t, err)
			catalog, err := parent.Catalog(ctx)
			require.NoError(t, err)
			require.Contains(t, catalog.Audio, "audio2")
			require.NotContains(t, catalog.Audio, "audio4", "old viewers must see only full-mix codec alternatives")
			var metadata struct {
				BatchDurationMS int `json:"batchDurationMs"`
				AudioStems      []struct {
					AlternateGroup uint32 `json:"alternateGroup"`
					Label          string `json:"label"`
				} `json:"audioStems"`
			}
			require.NoError(t, json.Unmarshal([]byte(catalog.Sections["streamplace"]), &metadata))
			require.Equal(t, 1000, metadata.BatchDurationMS)
			require.Len(t, metadata.AudioStems, 1)
			require.Equal(t, uint32(2), metadata.AudioStems[0].AlternateGroup)
			require.Equal(t, "Music: 音楽", metadata.AudioStems[0].Label)
			child, err := requestMoqBroadcast(ctx, client, "playback/"+streamer+"/audio-g2")
			require.NoError(t, err)
			childCatalog, err := child.Catalog(ctx)
			require.NoError(t, err)
			require.Empty(t, childCatalog.Video)
			require.Contains(t, childCatalog.Audio, "audio4")
			track, err := subscribeMoqTrack(ctx, child, "audio4", moqSourcePriority)
			require.NoError(t, err)
			defer track.Cancel()
			group, err := track.NextGroup(ctx)
			require.NoError(t, err)
			require.NotNil(t, group)
			var received []byte
			for frame, err := range group.Frames(ctx) {
				require.NoError(t, err)
				received = append(received, frame.Payload...)
			}
			group.Cancel()
			original, err := media.MuxlTracks(ctx, segments[0])
			require.NoError(t, err)
			require.Equal(t, original["audio4"], received, "stem CMAF retains its complete signed bytes")
			_, err = media.ValidateMP4MediaC2PA(ctx, received)
			require.NoError(t, err)
			renamed := signedGroupedMoqSegments(t, ctx, map[uint32]muxl.TrackMeta{2: {AlternateGroup: 1, Label: "Mix"}, 4: {AlternateGroup: 2, Label: "Music renamed"}})
			b.PublishSegment(ctx, streamer, media.PlaybackSourceRendition, &bus.Seg{Filepath: "renamed", Muxl: renamed[1], Published: true})
			nextPublic, err := track.NextGroup(ctx)
			require.NoError(t, err, "renaming a stem must not change its decoder configuration")
			require.NotNil(t, nextPublic)
			for _, err := range nextPublic.Frames(ctx) {
				require.NoError(t, err)
			}
			nextPublic.Cancel()
			regrouped := signedGroupedMoqSegments(t, ctx, map[uint32]muxl.TrackMeta{2: {AlternateGroup: 3, Label: "Voice"}, 4: {AlternateGroup: 1, Label: "Mix"}})
			b.PublishSegment(ctx, streamer, media.PlaybackSourceRendition, &bus.Seg{Filepath: "regrouped", Muxl: regrouped[1], Published: true})
			oldGroup, oldErr := track.NextGroup(ctx)
			require.Nil(t, oldGroup, "a removed stem must not remain available")
			if oldErr != nil {
				require.NotErrorIs(t, oldErr, context.DeadlineExceeded)
			}
			require.Eventually(t, func() bool {
				current, err := parent.Catalog(ctx)
				if err != nil {
					return false
				}
				var m struct {
					AudioStems []moqAudioStem `json:"audioStems"`
				}
				return json.Unmarshal([]byte(current.Sections["streamplace"]), &m) == nil && len(m.AudioStems) == 1 && m.AudioStems[0].AlternateGroup == 3 && len(current.Audio) == 1 && current.Audio["audio4"].Codec == "opus"
			}, time.Second, time.Millisecond, "full-source group changes retire the old main and stem tracks")
			child, err = requestMoqBroadcast(ctx, client, "playback/"+streamer+"/audio-g3")
			require.NoError(t, err)
			track, err = subscribeMoqTrack(ctx, child, "audio2", moqSourcePriority)
			require.NoError(t, err)
			defer track.Cancel()
			fresh, err := track.NextGroup(ctx)
			require.NoError(t, err)
			require.NotNil(t, fresh)
			var freshBytes []byte
			for frame, err := range fresh.Frames(ctx) {
				require.NoError(t, err)
				freshBytes = append(freshBytes, frame.Payload...)
			}
			fresh.Cancel()
			expected, err := media.MuxlTracks(ctx, regrouped[1])
			require.NoError(t, err)
			require.Equal(t, expected["audio2"], freshBytes)

			if ending == "private source" {
				b.PublishSegment(ctx, streamer, media.PlaybackSourceRendition, &bus.Seg{Filepath: "private", Muxl: segments[1], Published: false})
			} else {
				b.EndSession(ctx, streamer, media.PlaybackSourceRendition)
			}
			next, err := track.NextGroup(ctx)
			if err != nil {
				code, ok := official.ProtocolError(err)
				require.True(t, ok, "private child shutdown must be a protocol close")
				require.Equal(t, official.ProtocolKindApp, code.Kind)
				require.Equal(t, uint32(404+64), code.Code)
			}
			require.Nil(t, next, "a private source retires the child without publishing private samples")
			if ending == "private source" {
				require.Eventually(t, func() bool {
					current, err := parent.Catalog(ctx)
					if err != nil {
						return false
					}
					var m struct {
						AudioStems []moqAudioStem `json:"audioStems"`
					}
					return json.Unmarshal([]byte(current.Sections["streamplace"]), &m) == nil && len(m.AudioStems) == 0
				}, time.Second, time.Millisecond)
			}

		})
	}
}

func TestMoqAudioCatalogStillRejectsDecoderChanges(t *testing.T) {
	ctx := t.Context()
	source := signedSegments(t, ctx)[0]
	tracks, err := media.MuxlTracks(ctx, source)
	require.NoError(t, err)
	_, catalog, err := cmafFragments(tracks["audio2"])
	require.NoError(t, err)
	mode, err := cbor.DecOptions{DefaultMapType: reflect.TypeFor[map[string]any]()}.DecMode()
	require.NoError(t, err)
	for _, tc := range []struct {
		name       string
		key        string
		value      any
		compatible bool
	}{
		{"label", "label", "Renamed", true},
		{"group", "alternateGroup", uint64(2), true},
		{"codec", "codec", "mp4a.40.2", false},
		{"rate", "sampleRate", uint64(44100), false},
		{"decoder description", "description", []byte{1, 2, 3}, false},
		{"unknown codec field", "futureConfig", uint64(1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var root map[string]any
			require.NoError(t, mode.Unmarshal(catalog[24:], &root))
			configs := root["audio"].(map[string]any)["renditions"].(map[string]any)
			for _, config := range configs {
				config.(map[string]any)[tc.key] = tc.value
			}
			encoded, err := cbor.Marshal(root)
			require.NoError(t, err)
			changed := append(bytes.Clone(catalog[:24]), encoded...)
			binary.BigEndian.PutUint32(changed, uint32(len(changed)))
			require.Equal(t, tc.compatible, sameAudioDecoderCatalog(catalog, changed))
		})
	}
	require.False(t, sameAudioDecoderCatalog(catalog, catalog[:23]))
	_, videoCatalog, err := cmafFragments(tracks["video1"])
	require.NoError(t, err)
	require.False(t, sameAudioDecoderCatalog(videoCatalog, videoCatalog), "audio metadata compatibility must not apply to video catalogs")
}

// Rewrite the unsigned catalog before signing the unchanged encoded payload.
func customNamedMoqSegment(t *testing.T, ctx context.Context, source []byte, names map[string]string, metadata map[uint32]muxl.TrackMeta) []byte {
	t.Helper()
	original, err := media.MuxlTracks(ctx, source)
	require.NoError(t, err)
	signer, err := media.NewEphemeralMediaSigner("stem-streamer")
	require.NoError(t, err)
	signing, err := signer.SigningInput()
	require.NoError(t, err)
	mode, err := cbor.DecOptions{DefaultMapType: reflect.TypeFor[map[string]any]()}.DecMode()
	require.NoError(t, err)
	var out []byte
	for _, oldName := range []string{"video1", "audio2", "audio4"} {
		track := original[oldName]
		_, catalog, err := cmafFragments(track)
		require.NoError(t, err)
		var root map[string]any
		require.NoError(t, mode.Unmarshal(catalog[24:], &root))
		for _, kind := range []string{"audio", "video"} {
			section, ok := root[kind].(map[string]any)
			if !ok {
				continue
			}
			renditions := section["renditions"].(map[string]any)
			for key, raw := range renditions {
				config := raw.(map[string]any)
				id := uint32(config["container"].(map[string]any)["trackId"].(uint64))
				if kind == "audio" {
					if m, ok := metadata[id]; ok {
						config["alternateGroup"] = m.AlternateGroup
						config["label"] = m.Label
					}
				}
				if name, ok := names[key]; ok {
					delete(renditions, key)
					renditions[name] = config
				}
			}
		}
		encoded, err := cbor.CanonicalEncOptions().EncMode()
		require.NoError(t, err)
		body, err := encoded.Marshal(root)
		require.NoError(t, err)
		prefix := append(bytes.Clone(catalog[:24]), body...)
		binary.BigEndian.PutUint32(prefix, uint32(len(prefix)))
		offset := 0
		for offset < len(track) {
			size := int(binary.BigEndian.Uint32(track[offset:]))
			if bytes.Equal(track[offset:offset+size], catalog) {
				break
			}
			offset += size
		}
		require.Less(t, offset, len(track))
		unsigned := append(prefix, track[offset+len(catalog):]...)
		signed, err := muxl.RunMuxlSignTranscode(ctx, muxl.TranscodeInput{
			Output: unsigned, Source: track, CertPEM: signing.CertPEM, KeyPEM: signing.KeyPEM,
			Manifest: []byte(`{"title":"custom track fixture","assertions":[{"label":"cawg.metadata","data":{"@context":{"dc":"http://purl.org/dc/elements/1.1/"},"dc:creator":"stem-streamer","dc:title":"fixture","dc:date":"2026-11-15T12:00:00.000Z"}},{"label":"c2pa.actions","data":{"actions":[{"action":"c2pa.transcoded","parameters":{"org.cai.ingredientIds":["muxl.source"]}}]}}]}`),
		})
		require.NoError(t, err)
		out = append(out, signed...)
	}
	_, err = media.ValidateMP4MediaC2PA(ctx, out)
	require.NoError(t, err, "custom rendition names remain covered by valid signatures")
	return out
}

func TestMoqPlaybackCustomNamesFollowCatalogRoles(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	source := signedGroupedMoqSegments(t, ctx, map[uint32]muxl.TrackMeta{2: {AlternateGroup: 1, Label: "Mix"}, 4: {AlternateGroup: 2, Label: "Music"}})
	for _, tc := range []struct {
		name     string
		names    map[string]string
		metadata map[uint32]muxl.TrackMeta
		main     string
		video    string
		remove   string
	}{
		{name: "custom main becomes stem", names: map[string]string{"audio2": "mix", "audio4": "music"}, metadata: map[uint32]muxl.TrackMeta{2: {AlternateGroup: 3, Label: "Voice"}, 4: {AlternateGroup: 1, Label: "Mix"}}, main: "music", video: "video1"},
		{name: "custom main disappears", names: map[string]string{"audio2": "mix", "audio4": "music"}, metadata: map[uint32]muxl.TrackMeta{4: {AlternateGroup: 0, Label: "Mix"}}, main: "music", video: "video1", remove: "mix"},
		{name: "custom main label changes", names: map[string]string{"audio2": "mix", "audio4": "music"}, metadata: map[uint32]muxl.TrackMeta{2: {AlternateGroup: 1, Label: "Renamed mix"}}, main: "mix", video: "video1"},
		{name: "audio-looking video remains video", names: map[string]string{"video1": "audio-picture"}, main: "audio2", video: "audio-picture"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := customNamedMoqSegment(t, ctx, source[0], tc.names, nil)
			second := customNamedMoqSegment(t, ctx, source[1], tc.names, tc.metadata)
			if tc.remove != "" {
				parts, err := media.MuxlTracks(ctx, second)
				require.NoError(t, err)
				delete(parts, tc.remove)
				require.Len(t, parts, 2)
				second = append(bytes.Clone(parts[tc.video]), parts[tc.main]...)
				_, err = media.ValidateMP4MediaC2PA(ctx, second)
				require.NoError(t, err, "removing a whole track preserves remaining signatures")
			}
			b := bus.NewBus()
			const streamer = "did:plc:custom-names"
			b.PublishSegment(ctx, streamer, media.PlaybackSourceRendition, &bus.Seg{Filepath: "first", Muxl: first, Published: true})
			client := moqStemPlaybackClient(t, ctx, b)
			parent, err := requestMoqBroadcast(ctx, client, "playback/"+streamer)
			require.NoError(t, err)
			video, err := subscribeMoqTrack(ctx, parent, tc.video, moqSourcePriority)
			require.NoError(t, err)
			defer video.Cancel()
			group, err := video.NextGroup(ctx)
			require.NoError(t, err)
			require.NotNil(t, group)
			frame, err := group.ReadFrame(ctx)
			require.NoError(t, err)
			require.NotNil(t, frame)
			var mainAudio *official.TrackConsumer
			if tc.name == "custom main label changes" {
				mainAudio, err = subscribeMoqTrack(ctx, parent, "mix", moqSourcePriority)
				require.NoError(t, err)
				defer mainAudio.Cancel()
				initial, err := mainAudio.NextGroup(ctx)
				require.NoError(t, err)
				for _, err := range initial.Frames(ctx) {
					require.NoError(t, err)
				}
				initial.Cancel()
			}
			b.PublishSegment(ctx, streamer, media.PlaybackSourceRendition, &bus.Seg{Filepath: "second", Muxl: second, Published: true})
			if mainAudio != nil {
				nextAudio, err := mainAudio.NextGroup(ctx)
				require.NoError(t, err, "a custom audio name must allow display-label changes")
				require.NotNil(t, nextAudio)
				var received []byte
				for frame, err := range nextAudio.Frames(ctx) {
					require.NoError(t, err)
					received = append(received, frame.Payload...)
				}
				nextAudio.Cancel()
				want, err := media.MuxlTracks(ctx, second)
				require.NoError(t, err)
				require.Equal(t, want["mix"], received)
			}

			for _, err := range group.Frames(ctx) {
				require.NoError(t, err)
			}
			group.Cancel()
			require.Eventually(t, func() bool {
				catalog, err := parent.Catalog(ctx)
				return err == nil && len(catalog.Audio) == 1 && catalog.Audio[tc.main].Codec == "opus" && len(catalog.Video) == 1
			}, time.Second, time.Millisecond, "main selection must follow signed audio role, not the rendition name")
			next, err := video.NextGroup(ctx)
			require.NoError(t, err, "a video name beginning with audio must not retire its producer")
			require.NotNil(t, next)
			frame, err = next.ReadFrame(ctx)
			require.NoError(t, err)
			require.NotNil(t, frame)
			next.Cancel()
		})
	}
}
