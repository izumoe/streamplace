package vod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/blob"
	"stream.place/streamplace/pkg/comatproto"
	"stream.place/streamplace/pkg/muxl"
	"stream.place/streamplace/pkg/placestream"
	"stream.place/streamplace/pkg/statedb"
)

func TestRecordingPreservesCanonicalTrackBytes(t *testing.T) {
	store, err := blob.NewFileStore(t.TempDir())
	require.NoError(t, err)
	tracks := map[string][]byte{}
	for i, filename := range []string{"h264-opus-frag.mp4", "av1-aac-frag.mp4"} {
		fixture, err := os.ReadFile(getFixture(filename))
		require.NoError(t, err)
		ids := [][2]uint32{{7, 42}, {20, 99}}[i]
		canonical, err := muxl.RunMuxlCanonicalize(t.Context(), fixture, map[uint32]uint32{1: ids[0], 2: ids[1]})
		require.NoError(t, err)
		events := make(chan *muxl.MuxlEvent, 16)
		done := make(chan error, 1)
		go func() {
			done <- muxl.RunMuxlUnwrapEvents(t.Context(), bytes.NewReader(canonical), events)
			close(events)
		}()
		for event := range events {
			for id, data := range event.Tracks {
				if tracks[id] == nil {
					tracks[id] = data
				}
			}
		}
		require.NoError(t, <-done)
	}
	require.Len(t, tracks, 4)
	// Sparse canonical IDs also exercise physical order that differs from the
	// presentation grouping produced by unwrap.
	ids := []string{"20", "42", "7", "99"}
	var canonical bytes.Buffer
	for _, id := range ids {
		canonical.Write(tracks[id])
	}
	var input bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(t.Context(), bytes.NewReader(canonical.Bytes()), "fmp4", &input))
	signer, err := newUploadSigner(time.Now())
	require.NoError(t, err)
	events := make(chan *muxl.MuxlEvent, 16)
	segments := make(chan []byte, 16)
	signed := make(chan []byte, 1)
	done := make(chan error, 1)
	go func() {
		done <- muxl.RunMuxlSignSegment(t.Context(), bytes.NewReader(input.Bytes()), signer.SignerInput, nil, segments, events)
		close(events)
		close(segments)
	}()
	go func() {
		var data bytes.Buffer
		for segment := range segments {
			data.Write(segment)
		}
		signed <- data.Bytes()
	}()
	signedTracks := map[string][][]byte{}
	for event := range events {
		if event.Type == "signed-segment" {
			for id, data := range event.Tracks {
				signedTracks[id] = append(signedTracks[id], data)
			}
		}
	}
	require.NoError(t, <-done)
	require.Len(t, signedTracks, 4)
	canonical.Reset()
	canonical.Write(<-signed)
	verified, err := muxl.RunMuxlVerify(t.Context(), bytes.NewReader(canonical.Bytes()))
	require.NoError(t, err)
	var verification struct {
		Segments []struct {
			State string `json:"validation_state"`
		} `json:"segments"`
	}
	require.NoError(t, json.Unmarshal([]byte(verified), &verification))
	var count int
	for _, chunks := range signedTracks {
		count += len(chunks)
	}
	require.Len(t, verification.Segments, count)
	for _, segment := range verification.Segments {
		require.NotEqual(t, "Invalid", segment.State)
	}
	writer, err := store.NewWriter(t.Context(), "live/recording.m4s", "video/iso.segment")
	require.NoError(t, err)
	_, err = writer.Write(canonical.Bytes())
	require.NoError(t, err)
	require.NoError(t, writer.Complete())
	header, physicalMetafiles, err := synthFlatHeaderForObjects(t.Context(), store, []string{"live/recording.m4s"})
	require.NoError(t, err)
	for _, index := range []struct {
		name string
		data []byte
	}{
		{"missing", nil},
		{"truncated", physicalMetafiles[:len(physicalMetafiles)-1]},
		{"repeated", bytes.Repeat(physicalMetafiles, 2)},
	} {
		t.Run(index.name+" physical index", func(t *testing.T) {
			_, _, meta, err := hashAndBuildFragmentMetafile(t.Context(), store, []string{"live/recording.m4s"}, index.data)
			require.ErrorContains(t, err, "physical metafile")
			require.Nil(t, meta, "an incomplete index must not publish incorrect byte ranges")
		})
	}
	cid, _, meta, err := hashAndBuildFragmentMetafile(t.Context(), store, []string{"live/recording.m4s"}, physicalMetafiles)
	require.NoError(t, err)
	require.Len(t, meta.Tracks, 4)
	for id, track := range meta.Tracks {
		require.Len(t, track.Segments, len(signedTracks[id]))
		for i, segment := range track.Segments {
			require.Equal(t, int64(bytes.Index(canonical.Bytes(), signedTracks[id][i])), segment.Offset, "track %s fragment %d offset", id, i)
			require.True(t, bytes.Equal(signedTracks[id][i], canonical.Bytes()[segment.Offset:segment.Offset+segment.Size]), "track %s fragment %d byte range must identify its exact signed bytes", id, i)
		}
	}
	meta.FlatHeaderSize = int64(len(header))
	require.NoError(t, assembleContentBlob(t.Context(), store, header, []string{"live/recording.m4s"}, BlobsPrefix+cid+".mp4"))
	warmGST()
	thumbnail, err := generateThumbnail(t.Context(), store, cid, meta)
	require.NoError(t, err)
	image, err := jpeg.Decode(bytes.NewReader(thumbnail))
	require.NoError(t, err)
	require.Equal(t, 320, image.Bounds().Dx(), "thumbnail uses the larger H264 primary, not the 160px AV1 secondary")
	require.Equal(t, 240, image.Bounds().Dy())
	probe := probeFromMetafile(meta)
	encoded, err := marshalProbe(probe)
	require.NoError(t, err)
	var persisted struct {
		Tracks []struct {
			TrackID string `json:"trackId"`
		} `json:"tracks"`
	}
	require.NoError(t, json.Unmarshal([]byte(encoded), &persisted))
	var got []string
	for _, track := range persisted.Tracks {
		got = append(got, track.TrackID)
	}
	sort.Strings(got)
	require.Equal(t, []string{"20", "42", "7", "99"}, got)
}

type trackRecordClient struct {
	records []*placestream.MediaTrack
}

func (c *trackRecordClient) Do(_ context.Context, _ string, _ string, _ string, _ map[string]any, body any, out any) error {
	record := body.(comatproto.RepoPutRecord_Input).Record.Val.(*placestream.MediaTrack)
	c.records = append(c.records, record)
	result := out.(*comatproto.RepoPutRecord_Output)
	result.Uri = fmt.Sprintf("at://did:plc:test/place.stream.media.track/%d", len(c.records))
	result.Cid = fmt.Sprintf("record-%d", len(c.records))
	return nil
}

func TestPublishTracksPreservesCanonicalMetadata(t *testing.T) {
	state := newDraftTestState(t)
	upload := &statedb.Upload{ID: "tracks", RepoDID: "did:plc:test", ContentCID: "content", BlobSize: 1234,
		SigningKey: "did:key:wrong-global-key",
		ProbeJSON:  `{"durationMs":2000,"tracks":[{"trackId":"7","video":{"codec":"avc1.64002a","width":1920,"height":1080},"signingKey":"did:key:source"},{"trackId":"20","video":{"codec":"av01.0.08M.08","width":1280,"height":720}},{"trackId":"100","video":{"codec":"avc1.64001f","width":640,"height":360},"signingKey":"did:key:node"},{"trackId":"42","audio":{"codec":"mp4a.40.2","rate":48000,"channels":2}}]}`,
	}
	require.NoError(t, state.CreateUpload(t.Context(), upload))
	client := &trackRecordClient{}
	tracks, err := tracksForUpload(t.Context(), state, nil, client, upload.RepoDID, upload)
	require.NoError(t, err)
	require.NotNil(t, tracks)
	require.Len(t, tracks.Tracks, 4)
	require.Len(t, client.records, 4)
	for i, id := range []string{"7", "20", "100", "42"} {
		track := client.records[i].Track.MediaDefs_MuxlTrack
		require.Equal(t, id, track.TrackId)
		require.Equal(t, upload.ContentCID, track.Blob)
	}
	require.Equal(t, "h264", client.records[0].Metadata.MediaTrack_CommonMetadata.Video.Codec)
	require.Equal(t, "av1", client.records[1].Metadata.MediaTrack_CommonMetadata.Video.Codec)
	require.Equal(t, "h264", client.records[2].Metadata.MediaTrack_CommonMetadata.Video.Codec)
	require.Equal(t, "aac", client.records[3].Metadata.MediaTrack_CommonMetadata.Audio.Codec)
	require.Equal(t, "did:key:source", *client.records[0].Track.MediaDefs_MuxlTrack.SigningKey)
	require.Nil(t, client.records[1].Track.MediaDefs_MuxlTrack.SigningKey)
	require.Equal(t, "did:key:node", *client.records[2].Track.MediaDefs_MuxlTrack.SigningKey)
	stored, err := state.GetUpload(t.Context(), upload.ID)
	require.NoError(t, err)
	retry, err := tracksForUpload(t.Context(), state, nil, client, upload.RepoDID, stored)
	require.NoError(t, err)
	require.Equal(t, trackRefsJSON(tracks.Tracks), trackRefsJSON(retry.Tracks))
	require.Len(t, client.records, 4, "retry must reuse the already published track records")
}

func TestRecordedProbePreservesAllCanonicalTracks(t *testing.T) {
	meta := &Metafile{Tracks: map[string]MetafileTrack{
		"20": {Type: "video", Codec: "av01.0.08M.08", Timescale: 90000, Width: 2560, Height: 1440, Segments: []MetafileSegment{{DurationTicks: 90000}}},
		"7":  {Type: "video", Codec: "avc1.64002a", Timescale: 90000, Width: 1920, Height: 1080, Segments: []MetafileSegment{{DurationTicks: 180000}}},
		"42": {Type: "audio", Codec: "mp4a.40.2", Timescale: 48000, SampleRate: 48000, Channels: 2, Segments: []MetafileSegment{{DurationTicks: 96000}}},
	}}
	probe := probeFromMetafile(meta)
	encoded, err := marshalProbe(probe)
	require.NoError(t, err)
	var stored struct {
		DurationMS int64 `json:"durationMs"`
		Tracks     []struct {
			TrackID    string  `json:"trackId"`
			SigningKey *string `json:"signingKey"`
		} `json:"tracks"`
	}
	require.NoError(t, json.Unmarshal([]byte(encoded), &stored))
	require.Equal(t, int64(2000), stored.DurationMS)
	require.Len(t, stored.Tracks, 3)
	require.Equal(t, "20", stored.Tracks[0].TrackID)
	require.Equal(t, "7", stored.Tracks[1].TrackID)
	require.Equal(t, "42", stored.Tracks[2].TrackID)
	for _, track := range stored.Tracks {
		require.Nil(t, track.SigningKey, "the archive catalog does not prove a per-track signing key")
	}
}

func TestLegacyProbeUsesCanonicalTrackMetadata(t *testing.T) {
	state := newDraftTestState(t)
	store, err := blob.NewFileStore(t.TempDir())
	require.NoError(t, err)
	meta := &Metafile{Tracks: map[string]MetafileTrack{
		"7":  {Type: "video", Codec: "av01.0.08M.08", Timescale: 90000, Width: 160, Height: 120, Segments: []MetafileSegment{{DurationTicks: 90000}}},
		"42": {Type: "audio", Codec: "mp4a.40.2", Timescale: 48000, SampleRate: 48000, Channels: 2, Segments: []MetafileSegment{{DurationTicks: 48000}}},
	}}
	require.NoError(t, writeMetafile(t.Context(), store, "legacy-content", meta))
	upload := &statedb.Upload{ID: "legacy", ContentCID: "legacy-content", SigningKey: "did:key:unproven",
		ProbeJSON: `{"durationMs":1000,"video":{"codec":"h264","width":640,"height":360},"audio":{"codec":"mpeg","rate":48000,"channels":2}}`,
	}
	require.NoError(t, state.CreateUpload(t.Context(), upload))
	client := &trackRecordClient{}
	tracks, err := tracksForUpload(t.Context(), state, store, client, "did:plc:test", upload)
	require.NoError(t, err)
	require.Len(t, tracks.Tracks, 2)
	require.Equal(t, "7", client.records[0].Track.MediaDefs_MuxlTrack.TrackId)
	require.Equal(t, "42", client.records[1].Track.MediaDefs_MuxlTrack.TrackId)
	require.Equal(t, "av1", client.records[0].Metadata.MediaTrack_CommonMetadata.Video.Codec)
	require.Nil(t, client.records[0].Track.MediaDefs_MuxlTrack.SigningKey)

	upload.TrackURIs = ""
	upload.ContentCID = "missing"
	_, err = tracksForUpload(t.Context(), state, store, client, "did:plc:test", upload)
	require.ErrorContains(t, err, "read canonical track metadata")
	require.Len(t, client.records, 2, "missing metadata must not create guessed track records")
}

func TestPublishTracksRejectsInvalidMetadata(t *testing.T) {
	state := newDraftTestState(t)
	for _, probe := range []string{
		`{"tracks":[{"trackId":"0","video":{"codec":"av01.0.08M.08"}}]}`,
		`{"tracks":[{"trackId":"7","video":{"codec":"av01.0.08M.08"}},{"trackId":"7","video":{"codec":"avc1.64001f"}}]}`,
		`{"tracks":[{"trackId":"7","video":{"codec":"unknown"}}]}`,
		`{"tracks":[{"trackId":"42","audio":{"codec":"unknown"}}]}`,
		`{"tracks":[{"trackId":"7","video":{"codec":"av01.0.08M.08"},"audio":{"codec":"opus"}}]}`,
		`{`,
	} {
		client := &trackRecordClient{}
		_, err := tracksForUpload(t.Context(), state, nil, client, "did:plc:test", &statedb.Upload{ContentCID: "content", ProbeJSON: probe})
		require.Error(t, err)
		require.Empty(t, client.records, "validate the whole track set before publishing any records")
	}
}
