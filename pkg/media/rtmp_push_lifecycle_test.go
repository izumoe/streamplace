package media

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"stream.place/streamplace/pkg/log"
)

type rtmpPushReader struct {
	io.ReadCloser
	reading chan struct{}
	once    sync.Once
}

func (r *rtmpPushReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.reading) })
	return r.ReadCloser.Read(p)
}

func TestRTMPPushPipelineCancelsIdleSource(t *testing.T) {
	withNoGSTLeaks(t, func() {
		var logs logCapture
		previousLogger := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
		defer slog.SetDefault(previousLogger)
		destination, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer destination.Close()
		pr, pw := io.Pipe()
		defer pw.Close()
		source := &rtmpPushReader{ReadCloser: pr, reading: make(chan struct{})}
		ctx := log.WithLogValues(t.Context(), "test", t.Name())
		ctx = log.WithDebugValue(ctx, map[string]map[string]int{"test": {t.Name(): 3}})
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- (&MediaManager{}).runRTMPPushPipeline(ctx, source, "rtmp://"+destination.Addr().String()+"/live", func(string, string) {})
		}()
		defer func() {
			_ = pr.Close()
			cancel()
		}()
		select {
		case <-source.reading:
		case err := <-done:
			t.Fatalf("pipeline ended before requesting media: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("native appsrc never requested media")
		}
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(2 * time.Second):
			// Release the real blocked read so the failing test can still tear down.
			_ = pr.Close()
			require.Error(t, <-done)
			t.Fatal("native shutdown waited for more source media after cancellation")
		}
		require.NotContains(t, logs.String(), "level=ERROR", "source closure on cancellation is an expected stop")
	})
}

func TestRTMPPushPipelineErrorClosesSource(t *testing.T) {
	withNoGSTLeaks(t, func() {
		pr, pw := io.Pipe()
		defer pr.Close()
		defer pw.Close()
		err := (&MediaManager{}).runRTMPPushPipeline(t.Context(), pr, "http://127.0.0.1:1/live", func(string, string) {})
		require.ErrorContains(t, err, "invalid target URL scheme")
		written := make(chan error, 1)
		go func() {
			_, err := pw.Write([]byte("media"))
			written <- err
		}()
		select {
		case err := <-written:
			require.ErrorIs(t, err, io.ErrClosedPipe)
		case <-time.After(time.Second):
			_ = pr.Close()
			require.ErrorIs(t, <-written, io.ErrClosedPipe)
			t.Fatal("a rejected pipeline left its source writer blocked")
		}
	})
}

func TestRTMPForwarderClosesBothDirections(t *testing.T) {
	for _, end := range []string{"cancel", "publisher", "destination"} {
		t.Run(end, func(t *testing.T) {
			ignore := goleak.IgnoreCurrent()
			defer goleak.VerifyNone(t, ignore)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			destination, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer destination.Close()
			mm := &MediaManager{}
			address, err := mm.runForwarder(ctx, "rtmp://"+destination.Addr().String()+"/live", func(host string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", host)
			})
			require.NoError(t, err)
			publisher, err := net.Dial("tcp", address)
			require.NoError(t, err)
			defer publisher.Close()
			require.NoError(t, destination.(*net.TCPListener).SetDeadline(time.Now().Add(2*time.Second)))
			server, err := destination.Accept()
			require.NoError(t, err)
			defer server.Close()
			require.NoError(t, publisher.SetDeadline(time.Now().Add(2*time.Second)))
			require.NoError(t, server.SetDeadline(time.Now().Add(2*time.Second)))
			_, err = publisher.Write([]byte("source"))
			require.NoError(t, err)
			data := make([]byte, 6)
			_, err = io.ReadFull(server, data)
			require.NoError(t, err)
			require.Equal(t, "source", string(data))
			_, err = server.Write([]byte("target"))
			require.NoError(t, err)
			_, err = io.ReadFull(publisher, data)
			require.NoError(t, err)
			require.Equal(t, "target", string(data))
			switch end {
			case "cancel":
				cancel()
				_, err = publisher.Read(data)
				require.ErrorIs(t, err, io.EOF)
				_, err = server.Read(data)
				require.ErrorIs(t, err, io.EOF)
			case "publisher":
				require.NoError(t, publisher.Close())
				_, err = server.Read(data)
				require.ErrorIs(t, err, io.EOF)
			case "destination":
				require.NoError(t, server.Close())
				_, err = publisher.Read(data)
				require.ErrorIs(t, err, io.EOF)
			}
		})
	}
}

func TestRTMPForwarderCancelBackpressuredDestination(t *testing.T) {
	ignore := goleak.IgnoreCurrent()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	destination, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer destination.Close()
	mm := &MediaManager{}
	address, err := mm.runForwarder(ctx, "rtmp://"+destination.Addr().String()+"/live", func(host string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", host)
	})
	require.NoError(t, err)
	publisher, err := net.Dial("tcp", address)
	require.NoError(t, err)
	defer publisher.Close()
	require.NoError(t, publisher.(*net.TCPConn).SetWriteBuffer(1024))
	require.NoError(t, destination.(*net.TCPListener).SetDeadline(time.Now().Add(2*time.Second)))
	server, err := destination.Accept()
	require.NoError(t, err)
	defer server.Close()
	require.NoError(t, server.(*net.TCPConn).SetReadBuffer(1024))
	require.NoError(t, server.SetReadDeadline(time.Now().Add(2*time.Second)))
	written := make(chan error, 1)
	go func() {
		_, err := publisher.Write(make([]byte, 8<<20))
		written <- err
	}()
	_, err = io.ReadFull(server, make([]byte, 1024))
	require.NoError(t, err, "the connection must be forwarding before applying backpressure")
	select {
	case err := <-written:
		t.Fatalf("publisher did not encounter destination backpressure: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-written:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not interrupt the publisher")
	}
	// The destination stays open and does not drain its socket. Cancellation
	// must stop both forwarder copies without help from the remote endpoint.
	goleak.VerifyNone(t, ignore)
}

func TestRTMPForwarderCancelTLSHandshake(t *testing.T) {
	ignore := goleak.IgnoreCurrent()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	destination, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer destination.Close()
	mm := &MediaManager{}
	address, err := mm.RunTLSFForwarder(ctx, "rtmps://"+destination.Addr().String()+"/live")
	require.NoError(t, err)
	publisher, err := net.Dial("tcp", address)
	require.NoError(t, err)
	defer publisher.Close()
	require.NoError(t, destination.(*net.TCPListener).SetDeadline(time.Now().Add(2*time.Second)))
	server, err := destination.Accept()
	require.NoError(t, err)
	defer server.Close()
	require.NoError(t, server.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, err = io.ReadFull(server, make([]byte, 1))
	require.NoError(t, err, "a real TLS ClientHello must be in flight before cancelling")
	cancel()
	// The server keeps the connection open and never answers the handshake.
	goleak.VerifyNone(t, ignore)
}
