package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/crypto/spkey"
	"stream.place/streamplace/pkg/gstinit"
	"stream.place/streamplace/pkg/media"
)

func TestFixtureSignerUsesTheRegisteredStreamKey(t *testing.T) {
	key, pub, err := spkey.GenerateStreamKeyForDID("did:plc:fixture")
	require.NoError(t, err)
	signer, err := fixtureSigner(t.Context(), key, "did:plc:fixture")
	require.NoError(t, err)
	require.Equal(t, pub.DIDKey(), signer.DID())
	_, err = fixtureSigner(t.Context(), key, "did:plc:other")
	require.ErrorContains(t, err, "different account")
	for _, invalid := range []string{"", "invalid", "z1"} {
		_, err = fixtureSigner(t.Context(), invalid, "did:plc:fixture")
		require.Error(t, err)
	}
}

func TestPostingPreservesSignedAssetsAndReportsRejection(t *testing.T) {
	payload := []byte("already-signed-canonical-assets")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/segment", r.URL.Path)
		received := new(bytes.Buffer)
		_, err := received.ReadFrom(r.Body)
		require.NoError(t, err)
		require.Equal(t, payload, received.Bytes())
		if r.Header.Get("Content-Type") != "video/mp4" {
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	require.NoError(t, postSegment(context.Background(), server.Client(), server.URL, payload))
	rejected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "invalid signature", 400) }))
	defer rejected.Close()
	require.ErrorContains(t, postSegment(t.Context(), rejected.Client(), rejected.URL, payload), "400")
}

func TestSignedFixtureCarriesAllGroupsThroughTheReceiverParser(t *testing.T) {
	fixture := os.Getenv("E2E_AUDIO_STEMS_FIXTURE")
	if fixture == "" {
		t.Skip("generate the grouped fixture and set E2E_AUDIO_STEMS_FIXTURE")
	}
	key, pub, err := spkey.GenerateStreamKeyForDID("did:plc:fixture")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	gstinit.InitGST()
	type received struct {
		result *media.ValidationResult
		err    error
	}
	checked := make(chan received, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		var result *media.ValidationResult
		if err == nil {
			result, err = media.ValidateMP4Media(ctx, payload)
		}
		select {
		case checked <- received{result, err}:
		default:
		}
		if err != nil {
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	t.Setenv("SERVER_URL", server.URL)
	t.Setenv("ACCOUNT_DID", "did:plc:fixture")
	t.Setenv("E2E_STREAM_KEY", key)
	t.Setenv("E2E_AUDIO_STEMS_FIXTURE", fixture)
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	select {
	case result := <-checked:
		require.NoError(t, result.err)
		require.Equal(t, pub.DIDKey(), result.result.Pub.DIDKey())
		require.Len(t, result.result.MediaData.Video, 3)
		require.Len(t, result.result.MediaData.Audio, 3)
		for index, track := range result.result.MediaData.Audio {
			require.EqualValues(t, index+1, track.AlternateGroup)
			require.Equal(t, []string{"Mix", "Voice", "Music"}[index], track.Label)
		}
		require.Equal(t, "aac", result.result.MediaData.Audio[0].Codec)
		require.Equal(t, "opus", result.result.MediaData.Audio[1].Codec)
		require.Equal(t, "opus", result.result.MediaData.Audio[2].Codec)
	case err := <-done:
		t.Fatalf("publisher exited before a signed source: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			require.ErrorIs(t, err, context.Canceled)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fixture replay did not stop")
	}
}
