package websocketrep

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	glex "github.com/streamplace/glex/runtime"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/gstinit"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/moq"
	"stream.place/streamplace/pkg/placestream"
	"stream.place/streamplace/pkg/statedb"
)

func originView(streamer, wsURL string) *placestream.BroadcastDefs_BroadcastOriginView {
	return &placestream.BroadcastDefs_BroadcastOriginView{
		Record: &glex.LexiconTypeDecoder{Val: &placestream.BroadcastOrigin{
			Streamer:     streamer,
			WebsocketURL: &wsURL,
		}},
	}
}

// A pull whose origin refuses the first dials keeps trying and gets through
// once the origin is up; cancelling the pull ends it.
func TestPullRetriesUntilOriginAnswers(t *testing.T) {
	pullBackoffMin, pullBackoffMax = 5*time.Millisecond, 20*time.Millisecond
	var attempts atomic.Int32
	connected := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if attempts.Add(1) <= 3 {
			http.Error(w, "not yet", http.StatusServiceUnavailable)
			return
		}
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		connected <- struct{}{}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	r := &WebsocketReplicator{conns: map[string]bool{}, latest: map[string]latestOrigin{}}
	const streamer = "did:plc:streamer"
	r.rememberOrigin(originView(streamer, "ws"+strings.TrimPrefix(srv.URL, "http")+"/xrpc/place.stream.live.subscribeSegments"), streamer)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.pull(ctx, streamer)
		close(done)
	}()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("pull never got through to the origin")
	}
	require.GreaterOrEqual(t, attempts.Load(), int32(4), "three refusals, then the connection")
	require.True(t, r.hasConnection(streamer))
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pull did not stop on cancel")
	}
	require.False(t, r.hasConnection(streamer))
}

// A pull never gives up: an origin nobody has refreshed for a long time is
// still dialled, just slowly.
func TestPullKeepsRetryingQuietOrigin(t *testing.T) {
	pullBackoffMin, pullBackoffMax, pullBackoffQuiet = 5*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		attempts.Add(1)
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	r := &WebsocketReplicator{conns: map[string]bool{}, latest: map[string]latestOrigin{}}
	const streamer = "did:plc:streamer"
	r.rememberOrigin(originView(streamer, "ws"+strings.TrimPrefix(srv.URL, "http")+"/x"), streamer)
	r.latestMutex.Lock()
	r.latest[streamer] = latestOrigin{view: r.latest[streamer].view, seen: time.Now().Add(-originQuietAfter - time.Hour)}
	r.latestMutex.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.pull(ctx, streamer)
		close(done)
	}()
	require.Eventually(t, func() bool { return attempts.Load() >= 5 }, 5*time.Second, 5*time.Millisecond, "keeps dialling a quiet origin")
	cancel()
	<-done
}

// Rows from the shared statedb become origins to pull from, shaped like the
// firehose's, with the URL a node at that server DID advertises and the MoQ
// URL the row carries, if any.
func TestOriginViewForRow(t *testing.T) {
	r := &WebsocketReplicator{cli: &config.CLI{ServerHost: "me.example", BehindHTTPSProxy: true}}
	view := r.originViewForRow(statedb.BroadcastOrigin{StreamerRepoDID: "did:plc:s", ServerDID: "did:web:origin.example", UpdatedAt: time.Now()})
	origin := view.Record.Val.(*placestream.BroadcastOrigin)
	require.Equal(t, "wss://origin.example/xrpc/place.stream.live.subscribeSegments?streamer=did%3Aplc%3As", *origin.WebsocketURL)
	require.Equal(t, "did:plc:s", origin.Streamer)
	require.Equal(t, "did:plc:s", view.Author.Did)
	require.Nil(t, origin.MoqURL)

	view = r.originViewForRow(statedb.BroadcastOrigin{StreamerRepoDID: "did:plc:s", ServerDID: "did:web:origin.example", MoqURL: "moqt://origin.example:443?certhash=ab", UpdatedAt: time.Now()})
	require.Equal(t, "moqt://origin.example:443?certhash=ab", *view.Record.Val.(*placestream.BroadcastOrigin).MoqURL)
}

func TestRememberOriginKeepsNewestRecord(t *testing.T) {
	r := &WebsocketReplicator{cli: &config.CLI{}, latest: make(map[string]latestOrigin)}
	const did = "did:plc:streamer"
	now := time.Now().Truncate(time.Second)
	newer := r.originViewForRow(statedb.BroadcastOrigin{StreamerRepoDID: did, ServerDID: "did:web:new.example", UpdatedAt: now})
	older := r.originViewForRow(statedb.BroadcastOrigin{StreamerRepoDID: did, ServerDID: "did:web:old.example", UpdatedAt: now.Add(-time.Minute)})
	r.rememberOrigin(newer, did)
	r.rememberOrigin(older, did)
	latest, ok := r.latestFor(did)
	require.True(t, ok)
	require.Same(t, newer, latest.view, "replayed announcements must not move a stream back to its old origin")
	invalid := originView(did, "wss://old.example/xrpc/place.stream.live.subscribeSegments")
	r.rememberOrigin(invalid, did)
	latest, ok = r.latestFor(did)
	require.True(t, ok)
	require.Same(t, newer, latest.view, "a missing timestamp must not replace a known newer origin")
	newest := r.originViewForRow(statedb.BroadcastOrigin{StreamerRepoDID: did, ServerDID: "did:web:newest.example", UpdatedAt: now.Add(time.Minute)})
	r.rememberOrigin(newest, did)
	latest, ok = r.latestFor(did)
	require.True(t, ok)
	require.Same(t, newest, latest.view, "a newer announcement must still move the stream")
}

func TestDisallowedOriginsWithoutStatsAreNotRemembered(t *testing.T) {
	r := &WebsocketReplicator{
		cli:    &config.CLI{ServerHost: "me.example", AccessPolicy: "syndicate=off"},
		latest: make(map[string]latestOrigin),
	}
	const did = "did:plc:streamer"
	view := r.originViewForRow(statedb.BroadcastOrigin{
		StreamerRepoDID: did, ServerDID: "did:web:other.example", UpdatedAt: time.Now(),
	})
	require.False(t, r.cli.ShouldSyndicate(did))
	require.NoError(t, r.handleOriginMessage(t.Context(), view))
	_, ok := r.latestFor(did)
	require.False(t, ok, "a node without relay stats must not retain disallowed origins")
	require.False(t, r.hasConnection(did))
	local := r.originViewForRow(statedb.BroadcastOrigin{
		StreamerRepoDID: "did:plc:local", ServerDID: r.cli.ServerDID(), UpdatedAt: time.Now(),
	})
	require.NoError(t, r.handleOriginMessage(t.Context(), local))
	_, ok = r.latestFor("did:plc:local")
	require.False(t, ok, "local announcements must not be remembered as foreign ownership")
}

// The advertised MoQ URL is this host on the listener's port, carrying the
// self-signed certificate's hash for peers to pin, and nothing without a
// listener or relay.
func TestMyMoqURL(t *testing.T) {
	cert, err := moq.NewSelfSignedCert()
	require.NoError(t, err)
	r := &WebsocketReplicator{cli: &config.CLI{ServerHost: "me.example:38090", MoqAddr: ":38443"}, moqCert: cert}
	require.Equal(t, "moqt://me.example:38443?certhash="+cert.Hash(), r.getMyMoqURL())

	r = &WebsocketReplicator{cli: &config.CLI{ServerHost: "me.example", MoqAddr: ":443", MoqURL: "moqt://edge.example"}}
	require.Equal(t, "moqt://edge.example", r.getMyMoqURL())

	r = &WebsocketReplicator{cli: &config.CLI{ServerHost: "me.example"}, moqCert: cert}
	require.Equal(t, "", r.getMyMoqURL())
}

func TestMyMoqURLUsesRelayWithoutListenerCertificate(t *testing.T) {
	cert, err := moq.NewSelfSignedCert()
	require.NoError(t, err)
	for _, addr := range []string{"", ":38444"} {
		r := &WebsocketReplicator{cli: &config.CLI{
			ServerHost: "node.example", MoqAddr: addr,
			MoqRelayURL: "https://relay.example/anon/node", MoqURL: "moqt://private.example:38444",
		}, moqCert: cert}
		require.Equal(t, "https://relay.example/anon/node", r.getMyMoqURL())
	}
}

func TestOpenWebsocketRequestsFullSourcePreservingOriginQuery(t *testing.T) {
	requests := make(chan *http.Request, 1)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		requests <- req
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	origin := "ws" + strings.TrimPrefix(srv.URL, "http") + "/segments?streamer=did%3Aplc%3Atest&marker=retained"
	stream := originView("did:plc:test", origin).Record.Val.(*placestream.BroadcastOrigin)
	r := &WebsocketReplicator{}
	require.Error(t, r.openWebsocket(ctx, stream), "the peer's normal close ends the pull")
	select {
	case req := <-requests:
		query, err := url.ParseQuery(req.URL.RawQuery)
		require.NoError(t, err)
		require.Equal(t, "full", req.Header.Get("Streamplace-Source"))
		require.Empty(t, query.Get("source"), "capability negotiation must not add an undeclared XRPC parameter")
		require.Equal(t, "did:plc:test", query.Get("streamer"))
		require.Equal(t, "retained", query.Get("marker"))
		require.Equal(t, origin, *stream.WebsocketURL, "negotiation must not mutate the origin record")
	case <-ctx.Done():
		t.Fatal("the peer did not receive a websocket request")
	}
}

func TestWebsocketReplicationBindsSourceToNamedStreamer(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	gstinit.InitGST()
	signer, err := media.NewEphemeralMediaSigner("named-source")
	require.NoError(t, err)
	signer.PrebuiltManifest = []byte(fmt.Sprintf(`{"title":"source identity","assertions":[{"label":"c2pa.actions","data":{"actions":[{"action":"c2pa.created"}]}},{"label":"cawg.metadata","data":{"@context":{"dc":"http://purl.org/dc/elements/1.1/"},"dc:creator":%q,"dc:title":"source identity","dc:date":"1970-01-01T00:00:00.000Z"}}]}`, signer.DID()))
	source := signedSegmentsWithSigner(t, ctx, signer)[0]
	valid, err := media.ValidateMP4MediaC2PA(ctx, source)
	require.NoError(t, err)
	other, err := media.NewEphemeralMediaSigner("different-streamer")
	require.NoError(t, err)
	require.NotEqual(t, valid.Meta.Creator, other.DID())
	served := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			served <- err
			return
		}
		defer conn.Close()
		served <- conn.WriteMessage(websocket.BinaryMessage, source)
	}))
	defer srv.Close()
	cli := &config.CLI{BroadcasterHost: "node.test", ServerHost: "node.test", DataDir: t.TempDir(), Syndicate: []string{valid.Meta.Creator}}
	require.True(t, cli.ShouldSyndicate(valid.Meta.Creator))
	mm := media.NewOffline(cli)
	r := &WebsocketReplicator{mm: mm}
	u := "ws" + strings.TrimPrefix(srv.URL, "http")
	err = r.openWebsocket(ctx, &placestream.BroadcastOrigin{Streamer: other.DID(), WebsocketURL: &u})
	require.Zero(t, len(mm.BufferedSegments(valid.Meta.Creator)), "another streamer's valid media must not be admitted")
	require.ErrorContains(t, err, "not "+other.DID())
	require.NoError(t, <-served)
}
