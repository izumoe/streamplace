package director

import (
	"bytes"
	"context"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/localdb"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/s3"
)

func TestRecordingBackpressureDoesNotBlockNotificationCaller(t *testing.T) {
	for _, cutover := range []bool{false, true} {
		name := "segment"
		if cutover {
			name = "cutover"
		}
		t.Run(name, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var released, seen sync.Once
			unblock := func() { released.Do(func() { close(release) }) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				switch {
				case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
					seen.Do(func() { close(started) })
					<-release
					_, _ = io.WriteString(w, `<InitiateMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><UploadId>recording</UploadId></InitiateMultipartUploadResult>`)
				case r.Method == http.MethodPut:
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("ETag", `"part"`)
				case r.Method == http.MethodPost && r.URL.Query().Has("uploadId"):
					_, _ = io.Copy(io.Discard, r.Body)
					_, _ = io.WriteString(w, `<CompleteMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><ETag>"complete"</ETag></CompleteMultipartUploadResult>`)
				default:
					http.Error(w, "unsupported multipart operation", http.StatusBadRequest)
				}
			}))
			uploader := s3.NewS3Uploader(s3.Config{Endpoint: server.URL, Bucket: "recordings", Region: "us-east-1", AccessKeyID: "test", SecretAccessKey: "test"}, "did:example:source", "live/", time.Hour, nil)
			defer func() {
				unblock()
				require.NoError(t, uploader.Close(context.Background()))
				server.Close()
			}()
			var logs bytes.Buffer
			previousLogger := slog.Default()
			previousVerbosity := flag.Lookup("v").Value.String()
			require.NoError(t, flag.Set("v", "1"))
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer func() {
				slog.SetDefault(previousLogger)
				require.NoError(t, flag.Set("v", previousVerbosity))
			}()
			b := bus.NewBus()
			ss := &StreamSession{s3Uploader: uploader, bus: b}
			notification := &media.NewSegmentNotification{Segment: &localdb.Segment{StartTime: time.Now()}, Muxl: []byte("canonical-source")}
			completed := make(chan struct{})
			go func() {
				ss.s3Upload(t.Context(), notification)
				<-started
				// Fill the uploader's bounded live queue behind the held HTTP request.
				for range 16 {
					ss.s3Upload(t.Context(), notification)
				}
				if cutover {
					ss.s3Cutover(t.Context())
				} else {
					ss.s3Upload(t.Context(), notification)
				}
				b.PublishSegment(t.Context(), "did:example:unrelated", "source", &bus.Seg{Published: true})
				close(completed)
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("S3 request did not start")
			}
			select {
			case <-completed:
			case <-time.After(250 * time.Millisecond):
				unblock()
				select {
				case <-completed:
				case <-time.After(time.Second):
					t.Fatal("notification caller did not resume")
				}
				t.Fatal("slow recording storage blocked the shared notification caller")
			}
			require.True(t, b.HasSegments("did:example:unrelated", "source"), "the same caller can continue serving other streams")
			require.Contains(t, logs.String(), "queue is full")
		})
	}
}

func TestRecordingAudioOnlyNotificationCannotStartAnObject(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>unexpected recording object</Message></Error>`)
	}))
	defer server.Close()
	uploader := s3.NewS3Uploader(s3.Config{Endpoint: server.URL, Bucket: "recordings", Region: "us-east-1", AccessKeyID: "test", SecretAccessKey: "test"}, "did:example:source", "live/", time.Hour, nil)
	defer func() { require.NoError(t, uploader.Close(context.Background())) }()
	var logs bytes.Buffer
	previousLogger := slog.Default()
	previousVerbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "2"))
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer func() {
		slog.SetDefault(previousLogger)
		require.NoError(t, flag.Set("v", previousVerbosity))
	}()
	ss := &StreamSession{s3Uploader: uploader}
	ss.s3Upload(t.Context(), &media.NewSegmentNotification{
		Segment: &localdb.Segment{StartTime: time.Now()},
		Muxl:    []byte("signed-audio-only"), AudioOnly: true,
	})
	require.NoError(t, uploader.Close(t.Context()))
	require.Zero(t, requests.Load(), "an orphan audio notification must never open a recording object")
	require.Contains(t, logs.String(), "without recorded source")
}
