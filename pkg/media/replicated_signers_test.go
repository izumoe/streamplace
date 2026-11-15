package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/bluesky-social/indigo/atproto/identity"
	"net"
	"net/http"
	"net/http/httptest"
	"stream.place/streamplace/pkg/atproto"
	"testing"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/crypto/signers"
)

func TestReplicatedVideoRequiresAuthorizedSigner(t *testing.T) {
	ctx := t.Context()
	signer := audioSignerForTest(t)
	source := allSignedBareSegments(t, ctx, signer, getFixture("h264-opus-frag.mp4"))[0]
	valid, err := ValidateMP4MediaC2PA(ctx, source)
	require.NoError(t, err)
	events, err := unwrapMuxlEvents(ctx, source)
	require.NoError(t, err)
	cli := &config.CLI{BroadcasterHost: "node.test", Syndicate: []string{signer.Streamer()}}
	mm := NewOffline(cli)
	mm.bus = bus.NewBus()
	mm.nodeCert, mm.nodeKeyPEM = nodeSignerForTest(t)
	mm.nodeSignerOnce.Do(func() {})
	mm.bufferSegment(signer.Streamer(), valid.Meta.StartTime.Time(), true, false, source, 0, *valid.Manifest.Label)
	mm.rememberSourceAudio(signer.Streamer(), *valid.Manifest.Label, events)
	mm.feedLiveWindow(ctx, signer.Streamer(), source, valid.Meta.StartTime.Time(), true)
	input := renditionMP4(t, 160, 120)
	outsiderCert, outsiderKey := nodeSignerForTest(t)
	addendum, err := mm.mintVideoRenditions(ctx, source, []RenditionInput{{MP4: input}}, outsiderCert, outsiderKey)
	require.NoError(t, err)
	_, _, err = verifiedRenditions(ctx, addendum)
	require.NoError(t, err, "the outsider's signature is valid")
	sub := mm.bus.SubscribeSegment(ctx, signer.Streamer(), "120p")
	t.Cleanup(func() { mm.bus.UnsubscribeSegment(ctx, signer.Streamer(), "120p", sub) })
	require.ErrorIs(t, mm.ReceiveRenditions(ctx, signer.Streamer(), addendum), ErrUntrustedTranscoder, "a valid signature does not authorize another streamer's video")
	require.Empty(t, sub.C)

	combined := append(bytes.Clone(source), addendum...)
	_, _, err = mm.validateSource(ctx, combined, false, signer.Streamer())
	require.ErrorIs(t, err, ErrUntrustedTranscoder, "the same outsider must not enter through a completed source")
	inputSigner, err := signer.SigningInput()
	require.NoError(t, err)
	for _, authority := range []struct {
		name      string
		cert, key []byte
	}{
		{"own station", mm.nodeCert, mm.nodeKeyPEM},
		{"streamer", inputSigner.CertPEM, inputSigner.KeyPEM},
	} {
		t.Run(authority.name, func(t *testing.T) {
			data, err := mm.mintVideoRenditions(ctx, source, []RenditionInput{{MP4: input}}, authority.cert, authority.key)
			require.NoError(t, err)
			require.NoError(t, mm.ReceiveRenditions(ctx, signer.Streamer(), data))
			require.NotEmpty(t, sub.C)
			<-sub.C
			_, _, err = mm.validateSource(ctx, append(bytes.Clone(source), data...), false, signer.Streamer())
			if authority.name == "streamer" {
				require.ErrorContains(t, err, "segment metadata", "trust cannot bypass source metadata consistency")
			} else {
				require.NoError(t, err)
			}
		})
	}
	pub, err := signers.ParseES256KCert(outsiderCert)
	require.NoError(t, err)
	cli.TrustedTranscoders = []string{pub.DIDKey()}
	require.NoError(t, mm.ReceiveRenditions(ctx, signer.Streamer(), addendum))
	_, _, err = mm.validateSource(ctx, combined, false, signer.Streamer())
	require.NoError(t, err)
	corrupt := bytes.Clone(addendum)
	corrupt[len(corrupt)-1] ^= 1
	err = mm.ReceiveRenditions(ctx, signer.Streamer(), corrupt)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUntrustedTranscoder, "bad signatures remain validation failures")
}

func TestKeyCreatorMustMatchSigningCertificate(t *testing.T) {
	ctx := t.Context()
	signer := audioSignerForTest(t)
	victim := audioSignerForTest(t)
	signer.PrebuiltManifest = bytes.ReplaceAll(signer.PrebuiltManifest, []byte(signer.Streamer()), []byte(victim.DID()))
	source := allSignedBareSegments(t, ctx, signer, getFixture("h264-opus-frag.mp4"))[0]
	valid, err := ValidateMP4MediaC2PA(ctx, source)
	require.NoError(t, err, "the forged declaration still has a valid signature")
	require.Equal(t, victim.DID(), valid.Meta.Creator)
	require.NotEqual(t, victim.DID(), valid.Pub.DIDKey())
	matching := allSignedBareSegments(t, ctx, victim, getFixture("h264-opus-frag.mp4"))[0]
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprintf("local=%t", local), func(t *testing.T) {
			mm := NewOffline(&config.CLI{
				BroadcasterHost: "node.test", Syndicate: []string{victim.DID()}, AllowedStreams: []string{victim.DID()},
			})
			_, _, err := mm.validateSource(ctx, source, local, victim.DID())
			require.ErrorContains(t, err, "does not match signing certificate", "permission for a declared DID cannot authorize another signing key")
			_, _, err = mm.validateSource(ctx, matching, local, victim.DID())
			require.NoError(t, err, "the declared key's real source remains permitted")
		})
	}
}

func trustTranscoderForTest(t *testing.T, mm *MediaManager, cert []byte) {
	t.Helper()
	pub, err := signers.ParseES256KCert(cert)
	require.NoError(t, err)
	mm.cli.TrustedTranscoders = append(mm.cli.TrustedTranscoders, pub.DIDKey())
}

func TestReplicatedAudioTailRequiresAuthorizedSigner(t *testing.T) {
	ctx := t.Context()
	streamer := audioSignerForTest(t)
	sources := allSignedBareSegments(t, ctx, streamer, getFixture("ertmp-red-h264.mp4"))
	mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
	mm.nodeCert, mm.nodeKeyPEM = nodeSignerForTest(t)
	mm.nodeSignerOnce.Do(func() {})
	foreignCert, foreignKey := nodeSignerForTest(t)
	var foreignTail []byte
	var ownTail []byte
	for _, authority := range []struct {
		cert, key []byte
		foreign   bool
	}{
		{mm.nodeCert, mm.nodeKeyPEM, false},
		{foreignCert, foreignKey, true},
	} {
		encoder := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
		tr := encoder.newStreamTranscoder(ctx, "opus", authority.cert, authority.key, func(_ any, data []byte, tail bool) {
			if tail {
				if authority.foreign {
					foreignTail = data
				} else {
					ownTail = data
				}
			} else if !authority.foreign {
				valid, err := ValidateMP4MediaC2PA(ctx, data)
				require.NoError(t, err)
				mm.bufferSegment(streamer.Streamer(), valid.Meta.StartTime.Time(), true, false, data, 0, *valid.Manifest.Label)
			}
		})
		for _, source := range sources {
			require.NoError(t, tr.Feed(source, nil))
		}
		require.NoError(t, tr.Close())
	}
	require.NotEmpty(t, foreignTail)
	_, tail, err := verifiedRenditions(ctx, foreignTail)
	require.NoError(t, err, "the foreign tail has a valid signature")
	require.True(t, tail)
	before := mm.BufferedSegments(streamer.Streamer())
	require.ErrorIs(t, mm.ReceiveRenditions(ctx, streamer.Streamer(), foreignTail), ErrUntrustedTranscoder)
	require.Equal(t, before, mm.BufferedSegments(streamer.Streamer()))
	require.NoError(t, mm.ReceiveRenditions(ctx, streamer.Streamer(), ownTail), "the authorized tail retains its source and continuity checks")
}

func TestTrustedStationResolvesPublishedKey(t *testing.T) {
	ctx := t.Context()
	cert, _ := nodeSignerForTest(t)
	pub, err := signers.ParseES256KCert(cert)
	require.NoError(t, err)
	var station string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/.well-known/did.json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": station,
			"verificationMethod": []map[string]string{{
				"id": station + "#atproto", "type": "Multikey", "controller": station, "publicKeyMultibase": pub.Multibase(),
			}},
		})
	}))
	defer srv.Close()
	station = "did:web:station.example.com"
	transport := srv.Client().Transport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	transport.TLSClientConfig.ServerName = "127.0.0.1"
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		require.Equal(t, "station.example.com:443", address)
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	cli := &config.CLI{BroadcasterHost: "node.test", TrustedTranscoders: []string{station}}
	mm := NewOffline(cli)
	mm.atsync = &atproto.ATProtoSynchronizer{CLI: cli, PLCDirectory: &identity.BaseDirectory{HTTPClient: http.Client{Transport: transport}}}
	streamer := audioSignerForTest(t).DID()
	require.NoError(t, mm.trustTranscoder(ctx, pub.DIDKey(), streamer))
	other := audioSignerForTest(t).DID()
	require.ErrorIs(t, mm.trustTranscoder(ctx, other, streamer), ErrUntrustedTranscoder)
	cli.TrustedTranscoders = []string{}
	require.ErrorIs(t, mm.trustTranscoder(ctx, pub.DIDKey(), streamer), ErrUntrustedTranscoder)
}
