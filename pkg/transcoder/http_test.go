package transcoder

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/renditions"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "-job" {
		if os.Getenv("SP_TRANSCODER_TEST_SECRET") != "" {
			fmt.Fprintln(os.Stderr, "job inherited application credentials")
			os.Exit(2)
		}
		if err := RunJob(context.Background(), os.Args[2], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testProfiles() []renditions.Profile {
	return []renditions.Profile{
		{Name: "small", Width: 160, Height: 120, Bitrate: 200_000, Profile: "high"},
		{Name: "smaller", Width: 80, Height: 60, Bitrate: 100_000, Profile: "baseline"},
	}
}

func TestHTTPTranscoderRoundTrip(t *testing.T) {
	t.Setenv("SP_TRANSCODER_TEST_SECRET", "private-node-value")
	handler, err := NewHandler(1)
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	outputs, err := Transcode(ctx, server.URL+"/transcode", encodeFixture(t, "h264-opus-frag.mp4"), testProfiles())
	require.NoError(t, err)
	require.Len(t, outputs, 2)
	for i, output := range outputs {
		probe := probeEncoded(t, output)
		require.Equal(t, testProfiles()[i].Width, probe.Streams[0].Width)
		require.Equal(t, "opus", probe.Streams[1].CodecName)
	}
}

func TestClientRestoresRequestedOrder(t *testing.T) {
	profiles := testProfiles()
	outputs, err := encode(context.Background(), encodeFixture(t, "h264-opus-frag.mp4"), profiles)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		writer := multipart.NewWriter(w)
		w.Header().Set("Content-Type", "multipart/mixed; boundary="+writer.Boundary())
		require.NoError(t, writeOutputs(writer, []renditions.Profile{profiles[1], profiles[0]}, [][]byte{outputs[1], outputs[0]}))
	}))
	t.Cleanup(server.Close)
	actual, err := Transcode(context.Background(), server.URL, encodeFixture(t, "h264-opus-frag.mp4"), profiles)
	require.NoError(t, err)
	require.Equal(t, outputs, actual)
}

func TestHTTPTranscoderRejectsInvalidProfiles(t *testing.T) {
	handler, err := NewHandler(1)
	require.NoError(t, err)
	for _, profiles := range [][]renditions.Profile{
		nil,
		{testProfiles()[0], testProfiles()[0]},
		{{Name: "../bad", Width: 160, Height: 120, Bitrate: 200_000, Profile: "high"}},
		{{Name: "bad", Width: 159, Height: 120, Bitrate: 200_000, Profile: "high"}},
		{{Name: "bad", Width: 160, Height: 120, Bitrate: 200_000, Profile: "arbitrary pipeline"}},
		{{Name: "bad", Width: 160, Height: 120, Bitrate: 200_000, FPS: 30, Profile: "high"}},
	} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormField("profiles")
		require.NoError(t, err)
		require.NoError(t, json.NewEncoder(part).Encode(profiles))
		part, err = writer.CreateFormFile("source", "segment.mp4")
		require.NoError(t, err)
		_, err = part.Write([]byte("not media"))
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		request := httptest.NewRequest(http.MethodPost, "/transcode", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	}
}

func TestHTTPTranscoderRejectsBusyBeforeReadingBody(t *testing.T) {
	handler, err := NewHandler(1)
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = fmt.Fprintf(conn, "POST /transcode HTTP/1.1\r\nHost: localhost\r\nContent-Type: multipart/form-data; boundary=job\r\nContent-Length: 1024\r\nExpect: 100-continue\r\n\r\n")
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusContinue, response.StatusCode)
	_, err = Transcode(context.Background(), server.URL+"/transcode", []byte("source"), testProfiles())
	require.ErrorIs(t, err, ErrBusy)
	require.NoError(t, conn.Close())
	require.Eventually(t, func() bool {
		response, err := http.Post(server.URL+"/transcode", "multipart/form-data; boundary=bad", strings.NewReader("bad"))
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusBadRequest
	}, time.Second, 10*time.Millisecond)
}

func TestClientRejectsMismatchedResults(t *testing.T) {
	for _, names := range [][]string{{"small"}, {"small", "small"}, {"unknown", "smaller"}} {
		t.Run(strings.Join(names, ","), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				writer := multipart.NewWriter(w)
				w.Header().Set("Content-Type", "multipart/mixed; boundary="+writer.Boundary())
				for _, name := range names {
					part, err := writer.CreateFormFile(name, name+".mp4")
					require.NoError(t, err)
					_, err = part.Write([]byte("invalid response media"))
					require.NoError(t, err)
				}
				require.NoError(t, writer.Close())
			}))
			t.Cleanup(server.Close)
			outputs, err := Transcode(context.Background(), server.URL, []byte("source"), testProfiles())
			require.Error(t, err)
			require.Nil(t, outputs)
		})
	}
}
