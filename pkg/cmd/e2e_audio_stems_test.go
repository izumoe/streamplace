package cmd

import (
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/moq"
)

func TestE2EAudioStemOriginUsesTheActualListenerCertificate(t *testing.T) {
	cert, err := moq.NewSelfSignedCert()
	require.NoError(t, err)
	pair, err := cert.Certificate()
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "tls.crt")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]}), 0600))
	origin, err := e2eAudioStemOrigin(path, "127.0.0.1:38090", "did:plc:fixture", 38443)
	require.NoError(t, err)
	require.Equal(t, "moqt://127.0.0.1:38443?certhash="+cert.Hash(), *origin.MoqURL)
	require.Equal(t, "did:web:127.0.0.1:38090", origin.Server)
	require.Equal(t, "did:plc:fixture", origin.Streamer)
	require.NotNil(t, origin.WebsocketURL)
	require.Equal(t, "ws://127.0.0.1:38090/xrpc/place.stream.live.subscribeSegments?streamer=did%3Aplc%3Afixture", *origin.WebsocketURL)
	require.NoError(t, os.WriteFile(path, []byte("invalid"), 0600))
	_, err = e2eAudioStemOrigin(path, "127.0.0.1:38090", "did:plc:fixture", 38443)
	require.Error(t, err)
}
