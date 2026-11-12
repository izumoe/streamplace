package director

import (
	"bytes"
	"context"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/gstinit"
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
				require.Contains(t, captured.String(), "could not present supplied rendition")
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
			selected, err := media.ValidateMP4Media(ctx, got.Data)
			require.NoError(t, err)
			require.Len(t, selected.MediaData.Video, 1)
			require.EqualValues(t, 160, selected.MediaData.Video[0].Width)
			require.EqualValues(t, 120, selected.MediaData.Video[0].Height)
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
