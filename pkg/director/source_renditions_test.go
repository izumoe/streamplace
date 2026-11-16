package director

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/go-gst/go-gst/pkg/gst"
	app "github.com/go-gst/go-gst/pkg/gstapp"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/gstinit"
	"stream.place/streamplace/pkg/gstpipeline"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/muxl"
)

func TestSuppliedRenditionPublishesWithoutTranscoder(t *testing.T) {
	gstinit.InitGST()
	ctx := t.Context()
	large, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "h264-opus-frag.mp4"))
	require.NoError(t, err)
	small, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "ertmp-red-h264.mp4"))
	require.NoError(t, err)
	large, err = muxl.RunMuxlCanonicalize(ctx, large, map[uint32]uint32{2: 65539})
	require.NoError(t, err)
	small, err = muxl.RunMuxlCanonicalize(ctx, small, map[uint32]uint32{1: 65538})
	require.NoError(t, err)
	high, err := media.MuxlTracks(ctx, large)
	require.NoError(t, err)
	low, err := media.MuxlTracks(ctx, small)
	require.NoError(t, err)
	var unsigned, input bytes.Buffer
	unsigned.Write(high["video1"])
	unsigned.Write(low["video65538"])
	unsigned.Write(high["audio65539"])
	require.NoError(t, muxl.RunMuxlWrap(ctx, &unsigned, "fmp4", &input))
	signer, err := media.NewEphemeralMediaSigner("supplied-renditions")
	require.NoError(t, err)
	events := make(chan *muxl.MuxlEvent, 16)
	done := make(chan error, 1)
	go func() { done <- signer.SignSegmentStream(ctx, &input, events, nil); close(events) }()
	var source []byte
	var expectedFrames uint32
	for event := range events {
		if event.Type == "signed-segment" && source == nil {
			for _, id := range []string{"1", "65538", "65539"} {
				source = append(source, event.Tracks[id]...)
			}
			expectedFrames = event.SampleCounts["65538"]
		}
	}
	require.NoError(t, <-done)
	require.NotEmpty(t, source)
	require.Positive(t, expectedFrames)
	valid, err := media.ValidateMP4Media(ctx, source)
	require.NoError(t, err)
	require.Len(t, valid.MediaData.Video, 2)
	for _, tc := range []struct {
		name                            string
		published, missing, tail, newer bool
	}{
		{name: "public", published: true},
		{name: "private", published: false},
		{name: "missing supplied track", published: true, missing: true},
		{name: "audio continuation cache", published: true, tail: true},
		{name: "newer producer", published: true, newer: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			published := tc.published
			b := bus.NewBus()
			ss, notif := idleStreamSession(b)
			notif.Segment.MediaData = valid.MediaData
			notif.Segment.RepoDID = signer.Streamer()
			stream, err := notif.Segment.ToStreamplaceSegment()
			require.NoError(t, err)
			sessionCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ended := make(chan error, 1)
			go func() { ended <- ss.Start(sessionCtx, notif) }()
			<-ss.started
			lower := b.SubscribeSegment(ctx, stream.Creator, "120p")
			defer b.UnsubscribeSegment(ctx, stream.Creator, "120p", lower)
			var captured bytes.Buffer
			if tc.missing {
				for i := range stream.Video {
					if stream.Video[i].TrackId != nil && *stream.Video[i].TrackId == "65538" {
						id := "99999"
						stream.Video[i].TrackId = &id
					}
				}
				logger := slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(&captured, nil)))
				t.Cleanup(func() { slog.SetDefault(logger) })
				verbosity := flag.Lookup("v").Value.String()
				require.NoError(t, flag.Set("v", "2"))
				t.Cleanup(func() { require.NoError(t, flag.Set("v", verbosity)) })
			}
			require.NoError(t, ss.AddToWebRTC(ctx, stream, "source", &bus.Seg{Filepath: *valid.Manifest.Label, Muxl: source, Data: valid.Playable, Published: published}, nil))
			if tc.missing {
				require.Empty(t, lower.C)
				require.True(t, b.HasSegments(stream.Creator, "source"), "one unavailable supplied rung must not suppress the healthy source")
				require.Contains(t, captured.String(), "could not packetize supplied rendition")
				cancel()
				require.NoError(t, <-ended)
				return
			}
			var got *bus.Seg
			select {
			case got = <-lower.C:
			case <-time.After(time.Second):
				t.Fatal("supplied lower video was never published to WebRTC")
			}
			require.Equal(t, published, got.Published, "source visibility must carry through unchanged")
			require.Len(t, got.PacketizedData.Video, int(expectedFrames), "keep all original frames, not just a keyframe")
			require.NotEmpty(t, got.PacketizedData.Audio)
			require.True(t, bytes.Equal(source, got.Muxl), "packetization retains the original signed source without rewriting tracks")
			requireVideoDimensions(t, got, 160, 120)
			require.True(t, b.HasSegments(stream.Creator, "120p"))
			if tc.tail {
				b.PublishSegment(ctx, stream.Creator, "120p", &bus.Seg{Filepath: got.Filepath, AudioOnly: true, Published: published})
			}
			if tc.newer {
				b.PublishSegment(ctx, stream.Creator, "120p", &bus.Seg{Filepath: "newer-source", Published: published})
			}
			cancel()
			require.NoError(t, <-ended)
			require.Equal(t, tc.newer, b.HasSegments(stream.Creator, "120p"), "teardown clears its own frames and tails without erasing a newer producer")
		})
	}
}

func TestSuppliedRenditionsPreserveCompletedMainAndStems(t *testing.T) {
	gstinit.InitGST()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pipelineElement, err := gst.ParseLaunch(strings.Join([]string{
		"videotestsrc num-buffers=60 ! video/x-raw,width=640,height=360,framerate=30/1 ! x264enc key-int-max=30 tune=zerolatency speed-preset=ultrafast ! h264parse ! mp4mux name=mux fragment-duration=500 ! appsink name=sink",
		"videotestsrc num-buffers=60 ! video/x-raw,width=320,height=180,framerate=30/1 ! x264enc key-int-max=30 bframes=2 speed-preset=veryfast ! h264parse ! mux.",
		"videotestsrc num-buffers=60 ! video/x-raw,width=160,height=90,framerate=30/1 ! x264enc key-int-max=30 tune=zerolatency speed-preset=ultrafast ! h264parse ! mux.",
		"audiotestsrc num-buffers=94 samplesperbuffer=1024 ! audio/x-raw,rate=48000,channels=2 ! audioconvert ! fdkaacenc ! aacparse ! mux.",
		"audiotestsrc num-buffers=100 samplesperbuffer=960 ! audio/x-raw,rate=48000,channels=2 ! audioconvert ! opusenc ! opusparse ! mux.",
		"audiotestsrc wave=triangle num-buffers=100 samplesperbuffer=960 ! audio/x-raw,rate=48000,channels=2 ! audioconvert ! opusenc ! opusparse ! mux.",
		"audiotestsrc num-buffers=100 samplesperbuffer=960 ! audio/x-raw,rate=48000,channels=2 ! audioconvert ! opusenc ! opusparse ! mux.",
	}, "\n"))
	require.NoError(t, err)
	pipeline := pipelineElement.(gst.Pipeline)
	var presentation bytes.Buffer
	pipeline.GetByName("sink").(app.AppSink).SetCallbacks(&app.AppSinkCallbacks{NewSample: gstpipeline.WriterNewSample(ctx, &presentation)})
	busDone := make(chan error, 1)
	go func() { busDone <- gstpipeline.HandleBusMessages(ctx, pipeline) }()
	require.NotEqual(t, gst.StateChangeFailure, pipeline.SetState(gst.StatePlaying))
	defer pipeline.SetState(gst.StateNull)
	require.NoError(t, <-busDone)
	require.NotEqual(t, gst.StateChangeFailure, pipeline.SetState(gst.StateNull))
	signer, err := media.NewEphemeralMediaSigner("supplied-stems")
	require.NoError(t, err)
	signing, err := signer.SigningInput()
	require.NoError(t, err)
	signing.TrackMeta = map[uint32]muxl.TrackMeta{4: {AlternateGroup: 1, Label: "Mix"}, 5: {AlternateGroup: 2, Label: "Voice"}, 6: {AlternateGroup: 3, Label: "Music"}, 7: {AlternateGroup: 1, Label: "Mix"}}
	signing.TrackManifest = []byte(`{"title":"supplied stems","assertions":[{"label":"c2pa.actions","data":{"actions":[{"action":"c2pa.created"},{"action":"c2pa.published"}]}},{"label":"cawg.metadata","data":{"@context":{"dc":"http://purl.org/dc/elements/1.1/"},"dc:creator":"supplied-stems","dc:title":"supplied stems","dc:date":"2026-11-15T12:00:00.000Z"}}]}`)
	signing.WrapperManifest = signing.TrackManifest
	events := make(chan *muxl.MuxlEvent, 16)
	done := make(chan error, 1)
	go func() { done <- muxl.RunMuxlSignSegment(ctx, &presentation, signing, nil, nil, events); close(events) }()
	firstTracks := map[uint32][]byte{}
	expectedFrames := map[uint32]uint32{}
	for event := range events {
		if event.Type != "signed-segment" {
			continue
		}
		for id := uint32(1); id <= 7; id++ {
			if firstTracks[id] == nil && len(event.Tracks[fmt.Sprint(id)]) > 0 {
				firstTracks[id] = event.Tracks[fmt.Sprint(id)]
				if id <= 3 {
					expectedFrames[id] = event.SampleCounts[fmt.Sprint(id)]
				}
			}
		}
	}
	require.NoError(t, <-done)
	var source []byte
	for id := uint32(1); id <= 7; id++ {
		require.NotEmpty(t, firstTracks[id])
		source = append(source, firstTracks[id]...)
	}
	valid, err := media.ValidateMP4Media(ctx, source)

	require.NoError(t, err)
	require.Len(t, valid.MediaData.Video, 3)
	require.Len(t, valid.MediaData.Audio, 4)
	require.True(t, valid.MediaData.Video[1].BFrames, "the lower rung must exercise reordered video")
	b := bus.NewBus()
	ss, notification := idleStreamSession(b)
	notification.Segment.MediaData = valid.MediaData
	notification.Segment.RepoDID = signer.Streamer()
	stream, err := notification.Segment.ToStreamplaceSegment()
	require.NoError(t, err)
	sessionCtx, stop := context.WithCancel(ctx)
	ended := make(chan error, 1)
	go func() { ended <- ss.Start(sessionCtx, notification) }()
	<-ss.started
	defer func() { stop(); require.NoError(t, <-ended) }()
	require.NoError(t, ss.AddToWebRTC(ctx, stream, "source", &bus.Seg{Filepath: *valid.Manifest.Label, Muxl: source, Data: valid.Playable, Published: true}, nil))
	for _, rung := range []struct {
		name          string
		id            uint32
		width, height int
	}{
		{media.WebRTCSourceRendition, 1, 640, 360}, {"180p", 2, 320, 180}, {"90p", 3, 160, 90},
	} {
		segment := b.LatestSegment(stream.Creator, rung.name)
		require.NotNil(t, segment, rung.name)
		require.Len(t, segment.PacketizedData.Video, int(expectedFrames[rung.id]), rung.name)
		requireVideoDimensions(t, segment, rung.width, rung.height)
		require.NotEmpty(t, segment.PacketizedData.Audio, rung.name)
		require.Len(t, segment.PacketizedData.Stems, 2, "%s must carry Voice and Music alongside the full mix", rung.name)
		require.Equal(t, uint32(2), segment.PacketizedData.Stems[0].Group)
		require.Equal(t, uint32(3), segment.PacketizedData.Stems[1].Group)
		require.NotEmpty(t, segment.PacketizedData.Stems[0].Audio)
		require.NotEmpty(t, segment.PacketizedData.Stems[1].Audio)
	}
}

func requireVideoDimensions(t *testing.T, segment *bus.Seg, width, height int) {
	t.Helper()
	require.NotEmpty(t, segment.PacketizedData.Video)
	var au h264.AnnexB
	require.NoError(t, au.Unmarshal(segment.PacketizedData.Video[0].Data))
	for _, nalu := range au {
		if len(nalu) == 0 || h264.NALUType(nalu[0]&0x1f) != h264.NALUTypeSPS {
			continue
		}
		var sps h264.SPS
		require.NoError(t, sps.Unmarshal(nalu))
		require.Equal(t, width, sps.Width())
		require.Equal(t, height, sps.Height())
		return
	}
	t.Fatal("first playable video frame has no decoder configuration")
}
