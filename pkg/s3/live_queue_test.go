package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// multipartStore implements the HTTP multipart operations used by the real AWS
// client. Completed objects contain exactly the requested parts, in order.
type multipartStore struct {
	mu           sync.Mutex
	uploads      map[string]map[int][]byte
	objects      [][]byte
	failNextPart bool
	started      chan struct{}
	release      chan struct{}
	once         sync.Once
	seen         sync.Once
}

func (s *multipartStore) unblock() { s.once.Do(func() { close(s.release) }) }

func (s *multipartStore) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml")
	query := r.URL.Query()
	if r.Method == http.MethodPost && query.Has("uploads") {
		s.seen.Do(func() { close(s.started) })
		<-s.release
		s.mu.Lock()
		id := strconv.Itoa(len(s.uploads) + 1)
		s.uploads[id] = map[int][]byte{}
		s.mu.Unlock()
		_, _ = fmt.Fprintf(w, `<InitiateMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, id)
		return
	}
	id := query.Get("uploadId")
	if r.Method == http.MethodDelete {
		s.mu.Lock()
		delete(s.uploads, id)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodPut {
		part, err := strconv.Atoi(query.Get("partNumber"))
		if err != nil || part < 1 {
			http.Error(w, "invalid part", http.StatusBadRequest)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		if s.failNextPart {
			s.failNextPart = false
			s.mu.Unlock()
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>source part rejected</Message></Error>`)
			return
		}
		upload, exists := s.uploads[id]
		if exists {
			upload[part] = data
		}
		s.mu.Unlock()
		if !exists {
			http.Error(w, "unknown upload", http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", fmt.Sprintf(`"part-%d"`, part))
		return
	}
	if r.Method == http.MethodPost {
		var request struct {
			Parts []struct {
				Number int    `xml:"PartNumber"`
				ETag   string `xml:"ETag"`
			} `xml:"Part"`
		}
		if err := xml.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		upload, exists := s.uploads[id]
		if !exists || len(request.Parts) == 0 {
			http.Error(w, "unknown or empty upload", http.StatusBadRequest)
			return
		}
		var data []byte
		for i, part := range request.Parts {
			body, exists := upload[part.Number]
			if !exists || part.Number != i+1 || part.ETag != fmt.Sprintf(`"part-%d"`, part.Number) || i < len(request.Parts)-1 && len(body) < minPartSize {
				http.Error(w, "invalid multipart completion", http.StatusBadRequest)
				return
			}
			data = append(data, body...)
		}
		s.objects = append(s.objects, data)
		_, _ = io.WriteString(w, `<CompleteMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><ETag>"complete"</ETag></CompleteMultipartUploadResult>`)
		return
	}
	http.Error(w, "unsupported S3 operation", http.StatusBadRequest)
}

func liveHTTPUploader(t *testing.T, blocked bool, cutoverEvery time.Duration, recorder Recorder) (*S3Uploader, *multipartStore) {
	t.Helper()
	store := &multipartStore{uploads: map[string]map[int][]byte{}, started: make(chan struct{}), release: make(chan struct{})}
	if !blocked {
		store.unblock()
	}
	server := httptest.NewServer(http.HandlerFunc(store.serve))
	uploader := NewS3Uploader(Config{Endpoint: server.URL, Bucket: "recordings", Region: "us-east-1", AccessKeyID: "test", SecretAccessKey: "test"}, "did:example:streamer", "live/", cutoverEvery, recorder)
	t.Cleanup(func() {
		store.unblock()
		require.NoError(t, uploader.Close(context.Background()))
		server.Close()
	})
	return uploader, store
}

func TestLiveUploadQueuePreservesSourceTailAndCutover(t *testing.T) {
	uploader, store := liveHTTPUploader(t, false, time.Hour, nil)
	ctx := t.Context()
	for i, source := range [][]byte{[]byte("source-a"), []byte("source-b")} {
		stamp := time.Unix(1700000000, 0).Add(time.Duration(i) * time.Second)
		owned := bytes.Clone(source)
		require.NoError(t, uploader.TryAddSegment(ctx, owned, stamp))
		owned[0] ^= 1
		require.NoError(t, uploader.TryAddAudioAddendum(ctx, []byte("audio-tail"), stamp))
		require.NoError(t, uploader.TryCutover(ctx))
	}
	require.NoError(t, uploader.Close(ctx))
	store.mu.Lock()
	defer store.mu.Unlock()
	require.Equal(t, [][]byte{[]byte("source-aaudio-tail"), []byte("source-baudio-tail")}, store.objects)
}

func TestLiveUploadBackpressureFinishesOnlyAcceptedPrefix(t *testing.T) {
	for _, cutover := range []bool{false, true} {
		name := "segment"
		if cutover {
			name = "cutover"
		}
		t.Run(name, func(t *testing.T) {
			uploader, store := liveHTTPUploader(t, true, time.Hour, nil)
			ctx := t.Context()
			prefix := []byte("source-first\n")
			require.NoError(t, uploader.TryAddSegment(ctx, prefix, time.Now()))
			select {
			case <-store.started:
			case <-time.After(time.Second):
				t.Fatal("S3 request did not begin")
			}
			for i := 0; i < cap(uploader.segCh); i++ {
				data := []byte(fmt.Sprintf("audio-%d\n", i))
				require.NoError(t, uploader.TryAddSegment(ctx, data, time.Now()))
				prefix = append(prefix, data...)
			}
			rejected := make(chan error, 1)
			go func() {
				if cutover {
					rejected <- uploader.TryCutover(ctx)
				} else {
					rejected <- uploader.TryAddSegment(ctx, []byte("not-accepted"), time.Now())
				}
			}()
			select {
			case err := <-rejected:
				require.ErrorIs(t, err, ErrBackpressure)
			case <-time.After(250 * time.Millisecond):
				store.unblock()
				t.Fatalf("live recording waited for the blocked S3 request; returned: %v", <-rejected)
			}
			require.ErrorIs(t, uploader.TryAddSegment(ctx, []byte("after-gap"), time.Now()), ErrUploaderClosed)
			require.ErrorIs(t, uploader.TryCutover(ctx), ErrUploaderClosed)
			require.ErrorIs(t, uploader.AddSegment(ctx, []byte("blocking-after-gap"), time.Now()), ErrUploaderClosed)
			// Explicit teardown can race the automatic prefix drain safely.
			closed := make(chan error, 3)
			for range 3 {
				go func() { closed <- uploader.Close(ctx) }()
			}
			store.unblock()
			for range 3 {
				select {
				case err := <-closed:
					require.NoError(t, err)
				case <-time.After(time.Second):
					t.Fatal("accepted prefix did not drain")
				}
			}
			store.mu.Lock()
			defer store.mu.Unlock()
			require.Equal(t, [][]byte{prefix}, store.objects, "a recording must end at the accepted prefix, never continue after a missing command")
		})
	}
}

func TestLiveUploadTryCanRaceClose(t *testing.T) {
	uploader, store := liveHTTPUploader(t, true, time.Hour, nil)
	ctx := t.Context()
	prefix := []byte("source\n")
	require.NoError(t, uploader.TryAddSegment(ctx, prefix, time.Now()))
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("S3 request did not begin")
	}
	for i := 0; i < cap(uploader.segCh); i++ {
		data := []byte(fmt.Sprintf("audio-%d\n", i))
		require.NoError(t, uploader.TryAddSegment(ctx, data, time.Now()))
		prefix = append(prefix, data...)
	}
	start := make(chan struct{})
	results := make(chan error, 16)
	closed := make(chan error, 4)
	for i := range 16 {
		go func() {
			<-start
			if i%2 == 0 {
				results <- uploader.TryAddSegment(ctx, []byte("not-accepted"), time.Now())
			} else {
				results <- uploader.TryCutover(ctx)
			}
		}()
	}
	for range 4 {
		go func() { <-start; closed <- uploader.Close(ctx) }()
	}
	close(start)
	stops := 0
	for range 16 {
		select {
		case err := <-results:
			if errors.Is(err, ErrBackpressure) {
				stops++
			} else {
				require.ErrorIs(t, err, ErrUploaderClosed)
			}
		case <-time.After(250 * time.Millisecond):
			store.unblock()
			t.Fatal("live producer waited for concurrent Close's network drain")
		}
	}
	require.LessOrEqual(t, stops, 1, "only one producer can stop recording on backpressure")
	store.unblock()
	for range 4 {
		select {
		case err := <-closed:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("concurrent close did not finish")
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	require.Equal(t, [][]byte{prefix}, store.objects)
}

func TestLiveUploadCancelledCommandStopsRecording(t *testing.T) {
	for _, cutover := range []bool{false, true} {
		name := "segment"
		if cutover {
			name = "cutover"
		}
		t.Run(name, func(t *testing.T) {
			uploader, store := liveHTTPUploader(t, true, time.Hour, nil)
			prefix := []byte("accepted-source")
			require.NoError(t, uploader.TryAddSegment(t.Context(), prefix, time.Now()))
			select {
			case <-store.started:
			case <-time.After(time.Second):
				t.Fatal("S3 request did not begin")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			var err error
			if cutover {
				err = uploader.TryCutover(ctx)
			} else {
				err = uploader.TryAddSegment(ctx, []byte("cancelled-command"), time.Now())
			}
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorIs(t, uploader.TryAddSegment(t.Context(), []byte("after-missing-command"), time.Now()), ErrUploaderClosed)
			store.unblock()
			require.NoError(t, uploader.Close(t.Context()))
			store.mu.Lock()
			defer store.mu.Unlock()
			require.Equal(t, [][]byte{prefix}, store.objects)
		})
	}
}
