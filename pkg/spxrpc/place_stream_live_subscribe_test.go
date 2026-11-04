package spxrpc

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/muxl"
)

func TestSubscribeSegmentsSelectsCompatibleSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	fixture, err := os.ReadFile("../../test/fixtures/h264-opus-frag.mp4")
	require.NoError(t, err)
	base, err := muxl.RunMuxlCanonicalize(ctx, fixture, map[uint32]uint32{1: 1, 2: 3})
	require.NoError(t, err)
	tracks, err := media.MuxlTracks(ctx, base)
	require.NoError(t, err)
	other, err := muxl.RunMuxlCanonicalize(ctx, fixture, map[uint32]uint32{1: 2, 2: 3})
	require.NoError(t, err)
	extra, err := media.MuxlTracks(ctx, other)
	require.NoError(t, err)
	combined := bytes.Join([][]byte{tracks["video1"], extra["video2"], tracks["audio3"]}, nil)
	var input bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(combined), "fmp4", &input))
	streamer, source := signedWebsocketSource(t, ctx, input.Bytes())
	tracks, err = media.MuxlTracks(ctx, source)
	require.NoError(t, err)
	require.Equal(t, 3, len(tracks))
	compatible := bytes.Join([][]byte{tracks["video1"], tracks["audio3"]}, nil)

	for _, tc := range []struct {
		name         string
		sourceHeader string
		want         []byte
	}{
		{"compatible", "", compatible},
		{"full", "full", source},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := bus.NewBus()
			b.PublishSegment(ctx, streamer, "source", &bus.Seg{Muxl: []byte("private source"), Published: false})
			b.PublishSegment(ctx, streamer, "source", &bus.Seg{Muxl: source, Published: true})
			s := &Server{cli: &config.CLI{}, bus: b}
			e := echo.New()
			var logs bytes.Buffer
			e.Logger.SetOutput(&logs)
			finished := make(chan error, 1)
			e.GET("/segments", func(c echo.Context) error {
				finished <- s.handlePlaceStreamLiveSubscribeSegments(c)
				return nil
			})
			srv := httptest.NewServer(e)
			defer srv.Close()
			u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/segments?streamer=" + streamer
			headers := http.Header{}
			if tc.sourceHeader != "" {
				headers.Set("Streamplace-Source", tc.sourceHeader)
			}
			conn, _, err := websocket.DefaultDialer.DialContext(ctx, u, headers)
			require.NoError(t, err)
			defer conn.Close()
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
			kind, got, err := conn.ReadMessage()
			require.NoError(t, err)
			require.Equal(t, websocket.BinaryMessage, kind)
			require.True(t, bytes.Equal(tc.want, got), "private media stays withheld and published signed tracks match the requested source (%d bytes, got %d)", len(tc.want), len(got))
			require.NoError(t, conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)))
			select {
			case err := <-finished:
				require.ErrorContains(t, err, "websocket: close 1000")
				require.Contains(t, logs.String(), "websocket: close 1000")
			case <-ctx.Done():
				t.Fatal("replication handler did not stop after its peer closed")
			}
		})
	}
}

func TestSubscribeSegmentsClosesIncompatibleSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	fixture, err := os.ReadFile("../../test/fixtures/av1-aac-frag.mp4")
	require.NoError(t, err)
	streamer, source := signedWebsocketSource(t, ctx, fixture)
	b := bus.NewBus()
	b.PublishSegment(ctx, streamer, "source", &bus.Seg{Muxl: source, Published: true})
	s := &Server{cli: &config.CLI{}, bus: b}
	e := echo.New()
	var logs bytes.Buffer
	e.Logger.SetOutput(&logs)
	finished := make(chan error, 1)
	e.GET("/segments", func(c echo.Context) error {
		finished <- s.handlePlaceStreamLiveSubscribeSegments(c)
		return nil
	})
	srv := httptest.NewServer(e)
	defer srv.Close()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/segments?streamer=" + streamer
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u, nil)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err = conn.ReadMessage()
	require.Error(t, err)
	var networkError net.Error
	require.False(t, errors.As(err, &networkError) && networkError.Timeout(), "an incompatible source must close, not leave its peer waiting")
	select {
	case err := <-finished:
		require.Error(t, err)
		if !errors.Is(err, context.Canceled) {
			require.Contains(t, logs.String(), err.Error(), "the reader's expected close error is captured")
		}
	case <-time.After(time.Second):
		t.Fatal("the replication handler did not stop when its sender failed")
	}
}

func signedWebsocketSource(t *testing.T, ctx context.Context, input []byte) (string, []byte) {
	t.Helper()
	signer, err := media.NewEphemeralMediaSigner("websocket-source")
	require.NoError(t, err)
	events := make(chan *muxl.MuxlEvent, 16)
	signed := make(chan error, 1)
	go func() {
		signed <- signer.SignSegmentStream(ctx, bytes.NewReader(input), events)
		close(events)
	}()
	var source []byte
	for event := range events {
		if event.Type != "signed-segment" {
			continue
		}
		for _, id := range slices.Sorted(maps.Keys(event.Tracks)) {
			source = append(source, event.Tracks[id]...)
		}
	}
	require.NoError(t, <-signed)
	require.NotEmpty(t, source)
	return signer.Streamer(), source
}
