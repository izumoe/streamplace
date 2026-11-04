package websocketrep

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	official "moq.dev/moq"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/moq"
)

func TestMoqRelayServesTheBusWithoutListener(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cert, err := moq.NewSelfSignedCert()
	require.NoError(t, err)
	tlsFiles, err := moq.ManagedTLSFiles(t.TempDir(), cert.Certificate)
	require.NoError(t, err)
	require.NoError(t, tlsFiles.Sync())
	relay, err := official.Listen(ctx, "127.0.0.1:0", official.WithTLSCert(tlsFiles.CertPath), official.WithTLSKey(tlsFiles.KeyPath))
	require.NoError(t, err)
	defer relay.Close()
	relayDone := make(chan error, 1)
	go func() { relayDone <- relay.Serve(ctx) }()
	defer func() { cancel(); require.ErrorIs(t, <-relayDone, context.Canceled) }()

	relayURL := fmt.Sprintf("moqt://%s?certhash=%s", relay.LocalAddr(), cert.Hash())
	testMoqRelayMedia(t, ctx, cancel, relayURL)
}

func TestMoqPublicRelay(t *testing.T) {
	relayURL := os.Getenv("SP_MOQ_RELAY_TEST")
	if relayURL == "" {
		t.Skip("set SP_MOQ_RELAY_TEST to an isolated public relay namespace for synthetic media")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	testMoqRelayMedia(t, ctx, cancel, relayURL)
}

func testMoqRelayMedia(t *testing.T, ctx context.Context, cancel context.CancelFunc, relayURL string) {
	t.Helper()
	b := bus.NewBus()
	r := &WebsocketReplicator{bus: b, cli: &config.CLI{MoqRelayURL: relayURL}}
	segments := signedSegments(t, ctx)
	const streamer = "did:plc:relaytest"
	for _, channel := range []string{moqSourceChannel, media.PlaybackSourceRendition} {
		b.PublishSegment(ctx, streamer, channel, &bus.Seg{Muxl: segments[0], Published: true})
	}
	done := make(chan error, 1)
	go func() { done <- r.serveMoQ(ctx) }()
	defer func() { cancel(); require.ErrorIs(t, <-done, context.Canceled) }()

	client, err := r.dialMoq(ctx, relayURL)
	require.NoError(t, err)
	defer client.Close()
	broadcast, err := requestMoqBroadcast(ctx, client, streamer)
	require.NoError(t, err)
	received := make(chan []byte, 1)
	consumed := make(chan error, 1)
	go func() {
		consumed <- consumeBroadcast(ctx, broadcast, func(segment []byte) error {
			select {
			case received <- segment:
			case <-ctx.Done():
			}
			return nil
		}, func(context.Context, []byte) error { return nil })
	}()
	defer func() { cancel(); require.ErrorIs(t, <-consumed, context.Canceled) }()
	select {
	case got := <-received:
		require.Equal(t, segments[0], got, "the relay preserves the signed source bytes")
	case <-ctx.Done():
		t.Fatal("no signed media arrived through the relay")
	}
	playback, err := requestMoqBroadcast(ctx, client, "playback/"+streamer)
	require.NoError(t, err)
	catalog, err := playback.Catalog(ctx)
	require.NoError(t, err)
	require.Contains(t, catalog.Video, "video1")
	require.Contains(t, catalog.Audio, "audio2")
	require.NoError(t, ctx.Err())
}

func TestMoqStartReportsListenerError(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	config.DisableSQLLogging()
	mod, err := model.MakeDBConns(t.TempDir(), 1)
	require.NoError(t, err)
	db, err := mod.(*model.DBModel).DB.DB()
	require.NoError(t, err)
	defer db.Close()
	r := NewWebsocketReplicator(bus.NewBus(), mod, nil, nil, nil)
	err = r.Start(ctx, &config.CLI{ServerHost: "localhost", MoqAddr: "127.0.0.1:0"})
	require.ErrorContains(t, err, "moq listener has no TLS files")
	require.NoError(t, ctx.Err(), "the listener error must reach the caller before the deadline")
}

func TestMoqRelayReconnects(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cert, err := moq.NewSelfSignedCert()
	require.NoError(t, err)
	tlsFiles, err := moq.ManagedTLSFiles(t.TempDir(), cert.Certificate)
	require.NoError(t, err)
	require.NoError(t, tlsFiles.Sync())
	relay, err := official.Listen(ctx, "127.0.0.1:0", official.WithTLSCert(tlsFiles.CertPath), official.WithTLSKey(tlsFiles.KeyPath))
	require.NoError(t, err)
	defer relay.Close()
	sessions := make(chan *official.Session, 2)
	acceptErrors := make(chan error, 1)
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for req, err := range relay.Requests(ctx) {
			if err != nil {
				return
			}
			session, err := req.Accept(ctx)
			if err != nil {
				acceptErrors <- err
				return
			}
			select {
			case sessions <- session:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); <-accepted }()
	r := &WebsocketReplicator{cli: &config.CLI{MoqRelayURL: fmt.Sprintf("moqt://%s?certhash=%s", relay.LocalAddr(), cert.Hash())}}
	done := make(chan error, 1)
	go func() { done <- r.publishMoQRelay(ctx, official.NewOriginProducer()) }()
	defer func() { cancel(); require.ErrorIs(t, <-done, context.Canceled) }()
	for connection := range 2 {
		select {
		case session := <-sessions:
			if connection == 0 {
				session.Cancel(0)
			}
		case err := <-acceptErrors:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal("relay publisher did not reconnect after its session was closed")
		}
	}
}
