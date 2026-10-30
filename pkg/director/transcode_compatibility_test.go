package director

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/aqtime"
	"stream.place/streamplace/pkg/atproto"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/gstinit"
	"stream.place/streamplace/pkg/localdb"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/muxl"
	"stream.place/streamplace/pkg/renditions"
	"stream.place/streamplace/pkg/statedb"
	"stream.place/streamplace/pkg/transcoder"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "-job" {
		if err := transcoder.RunJob(context.Background(), os.Args[2], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestAV1CompatibilityRenditionFeedsLegacyTransports(t *testing.T) {
	gstinit.InitGST()
	require.NoError(t, muxl.Initialize())
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cli := &config.CLI{BroadcasterHost: "node.test", ServerHost: "node.test", DBURL: ":memory:", DataDir: t.TempDir()}
	mod, err := model.MakeDB(":memory:")
	require.NoError(t, err)
	state, err := statedb.MakeDB(ctx, cli, nil, mod)
	require.NoError(t, err)
	repo, err := atproto.MakeServerRepo(ctx, cli, state)
	require.NoError(t, err)
	defer repo.Close()
	handler, err := transcoder.NewHandler(1)
	require.NoError(t, err)
	service := httptest.NewServer(handler)
	defer service.Close()
	cli.TranscoderURL = service.URL + "/transcode"
	av1, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "av1-aac-frag.mp4"))
	require.NoError(t, err)
	canonical, err := muxl.RunMuxlCanonicalize(ctx, av1, nil)
	require.NoError(t, err)
	opus, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "h264-opus-frag.mp4"))
	require.NoError(t, err)
	opusCanonical, err := muxl.RunMuxlCanonicalize(ctx, opus, map[uint32]uint32{2: 3})
	require.NoError(t, err)
	var audio bytes.Buffer
	audioEvents := make(chan *muxl.MuxlEvent, 16)
	audioErr := make(chan error, 1)
	go func() {
		audioErr <- muxl.RunMuxlUnwrapEvents(ctx, bytes.NewReader(opusCanonical), audioEvents)
		close(audioEvents)
	}()
	for event := range audioEvents {
		if event.Type == "segment" {
			audio.Write(event.Tracks["3"])
		}
	}
	require.NoError(t, <-audioErr)
	canonical = append(canonical, audio.Bytes()...)
	var presentation bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(canonical), "fmp4", &presentation))
	signer, err := media.NewEphemeralMediaSigner("test-streamer")
	require.NoError(t, err)
	events := make(chan *muxl.MuxlEvent, 16)
	signErr := make(chan error, 1)
	go func() {
		signErr <- signer.SignSegmentStream(ctx, bytes.NewReader(presentation.Bytes()), events)
		close(events)
	}()
	var source []byte
	for event := range events {
		if event.Type == "signed-segment" {
			for _, id := range []string{"1", "2", "3"} {
				source = append(source, event.Tracks[id]...)
			}
		}
	}
	require.NoError(t, <-signErr)
	validated, err := media.ValidateMP4Media(ctx, source)
	require.NoError(t, err)
	require.Len(t, validated.MediaData.Video, 1)
	require.Equal(t, "av1", validated.MediaData.Video[0].Codec)
	require.Len(t, validated.MediaData.Audio, 2, "the service input contains both source AAC and completed Opus")
	row := &localdb.Segment{RepoDID: "did:example:compatibility", MediaData: validated.MediaData}
	segment, err := row.ToStreamplaceSegment()
	require.NoError(t, err)
	segment.Creator = row.RepoDID
	profiles, err := renditions.GenerateRenditionsFrom(segment, cli.RenditionLadder())
	require.NoError(t, err)
	require.NotEmpty(t, profiles, "AV1 needs a compatible H264 rendition")
	b := bus.NewBus()
	ss := &StreamSession{cli: cli, bus: b, mm: media.NewOffline(cli), transcodeSlots: make(chan struct{}, 2)}
	webrtc := b.SubscribeSegment(ctx, segment.Creator, media.WebRTCSourceRendition)
	defer b.UnsubscribeSegment(ctx, segment.Creator, media.WebRTCSourceRendition, webrtc)
	rtmp := b.SubscribeSegment(ctx, segment.Creator, media.RTMPSourceRendition)
	defer b.UnsubscribeSegment(ctx, segment.Creator, media.RTMPSourceRendition, rtmp)
	notification := &media.NewSegmentNotification{Muxl: source, Data: validated.Playable, Metadata: &media.SegmentMetadata{Published: true, StartTime: aqtime.FromTime(time.Now())}}
	require.NoError(t, ss.Transcode(ctx, segment, notification, nil))
	require.Len(t, webrtc.C, 1, "the generic service supplies H264+Opus to default WHEP")
	require.NotEmpty(t, (<-webrtc.C).PacketizedData.Video)
	require.Len(t, rtmp.C, 1, "egress receives H264 with the original AAC")
	completed := <-rtmp.C
	require.True(t, bytes.HasPrefix(completed.Muxl, source), "original AV1 signatures remain unchanged")
	result, err := media.ValidateMP4Media(ctx, completed.Muxl)
	require.NoError(t, err)
	require.Len(t, result.MediaData.Video, 2)
	require.Len(t, result.MediaData.Audio, 2)
}
