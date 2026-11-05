package websocketrep

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	official "moq.dev/moq"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/moq"
	"stream.place/streamplace/pkg/muxl"
	"stream.place/streamplace/pkg/statedb"
)

func TestMoqViewerSnapshot(t *testing.T) {
	const prefix = "tenant/playback/"
	for _, tc := range []struct {
		name, body string
		want       map[string]int
	}{
		{"canonical", `{"tenant/playback/did:plc:a":{"broadcasts_started":5,"broadcasts_ended":2,"subscriptions_started":20},"tenant/source/did:plc:a":{"broadcasts_started":99},"other/playback/did:plc:a":{"broadcasts_started":42}}`, map[string]int{"did:plc:a": 3}},
		{"legacy", `{"tenant/playback/did:plc:a":{"broadcasts":3,"broadcasts_closed":1}}`, map[string]int{"did:plc:a": 2}},
		{"canonical zero wins", `{"tenant/playback/did:plc:a":{"broadcasts_started":0,"broadcasts":99}}`, map[string]int{"did:plc:a": 0}},
		{"reset", `{"tenant/playback/did:plc:a":{"broadcasts_started":1}}`, map[string]int{"did:plc:a": 1}},
		{"empty", `{}`, map[string]int{}},
		{"underflow", `{"tenant/playback/did:plc:a":{"broadcasts_started":1,"broadcasts_ended":2}}`, nil},
		{"negative", `{"tenant/playback/did:plc:a":{"broadcasts_started":-1}}`, nil},
		{"overflow", `{"tenant/playback/did:plc:a":{"broadcasts_started":18446744073709551615}}`, nil},
		{"null", `null`, nil},
		{"null entry", `{"tenant/playback/did:plc:a":null}`, nil},
		{"invalid", `not json`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := moqViewerSnapshot([]byte(tc.body), prefix)
			if tc.want == nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}
		})
	}
}

func TestMoqStatsRequireEveryNode(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	const did = "did:plc:moq-cluster"
	b := bus.NewBus()
	cli := &config.CLI{ServerHost: "origin.example", MoqRelayURL: "https://relay.example/tenant", MoqStatsTier: "viewer", AccessPolicy: "syndicate=off"}
	mm, err := media.MakeMediaManager(ctx, cli, nil, nil, b, nil, nil)
	require.NoError(t, err)
	state, err := statedb.MakeDB(ctx, &config.CLI{DBURL: ":memory:"}, nil, nil)
	require.NoError(t, err)
	var queries atomic.Int64
	require.NoError(t, state.DB.Callback().Query().Before("gorm:query").Register("count-origin-queries", func(tx *gorm.DB) {
		if tx.Statement.Table == "broadcast_origins" {
			queries.Add(1)
		}
	}))
	fixture, err := os.ReadFile("../../../test/fixtures/h264-opus-frag.mp4")
	require.NoError(t, err)
	canonical, err := muxl.RunMuxlCanonicalize(ctx, fixture, nil)
	require.NoError(t, err)
	for _, stream := range []string{did, "did:plc:foreign", "did:plc:unknown", "did:plc:preview", "did:plc:stale"} {
		require.NoError(t, mm.FeedLiveRenditions(ctx, stream, canonical, time.Now(), stream != "did:plc:preview"))
		if stream == "did:plc:unknown" {
			continue
		}
		owner, updated := cli.ServerDID(), time.Now()
		if stream == "did:plc:foreign" {
			owner = "did:web:other.example"
		}
		if stream == "did:plc:stale" {
			updated = updated.Add(-time.Hour)
		}
		require.NoError(t, state.UpsertBroadcastOrigin(stream, owner, cli.MoqRelayURL, updated))
	}
	mm.TouchMoqSession(did, "player")
	defer mm.CloseMoqSession(did, "player")
	cert, err := moq.NewSelfSignedCert()
	require.NoError(t, err)
	tls, err := moq.ManagedTLSFiles(t.TempDir(), cert.Certificate)
	require.NoError(t, err)
	require.NoError(t, tls.Sync())
	tracks := make([]*official.JSONSnapshotProducer, 2)
	stops := make([]context.CancelFunc, 2)
	for node := range tracks {
		origin := official.NewOriginProducer()
		broadcast, err := origin.CreateBroadcast("")
		require.NoError(t, err)
		zero := uint32(0)
		tracks[node], err = broadcast.PublishJSONSnapshot("viewer/publisher.json", official.JSONSnapshotOptions{DeltaRatio: &zero})
		require.NoError(t, err)
		require.NoError(t, broadcast.Announce(official.Route{}))
		server, err := official.Listen(ctx, "127.0.0.1:0", official.WithTLSCert(tls.CertPath), official.WithTLSKey(tls.KeyPath), official.WithServerPublishOrigin(origin))
		require.NoError(t, err)
		served := make(chan error, 1)
		nodeCtx, stop := context.WithCancel(ctx)
		stops[node] = stop
		go func() { served <- server.Serve(nodeCtx) }()
		defer func() {
			stop()
			require.NoError(t, server.Close())
			if err := <-served; err != nil {
				require.ErrorIs(t, err, context.Canceled)
			}
		}()
		cli.MoqStatsURLs = append(cli.MoqStatsURLs, fmt.Sprintf("moqt://%s/.stats/node/%d?certhash=%s", server.LocalAddr(), node, cert.Hash()))
	}
	r := &WebsocketReplicator{cli: cli, mm: mm, state: state, latest: make(map[string]latestOrigin)}
	done := make(chan error, 1)
	go func() { done <- r.runMoqViewerStats(ctx) }()
	defer func() { cancel(); require.ErrorIs(t, <-done, context.Canceled) }()
	publish := func(node, count int) {
		t.Helper()
		require.NoError(t, tracks[node].Update(map[string]any{
			"tenant/playback/" + did:          map[string]int{"broadcasts_started": count},
			"tenant/playback/did:plc:foreign": map[string]int{"broadcasts_started": 7},
			"tenant/playback/did:plc:unknown": map[string]int{"broadcasts_started": 7},
			"tenant/playback/did:plc:preview": map[string]int{"broadcasts_started": 7},
			"tenant/playback/did:plc:stale":   map[string]int{"broadcasts_started": 7},
		}))
	}
	publish(0, 5)
	// A node with no initial snapshot cannot contribute an authoritative zero.
	require.Never(t, func() bool { return b.GetViewerCount(did) != 1 }, 200*time.Millisecond, 10*time.Millisecond)
	publish(1, 3)
	require.Eventually(t, func() bool { return b.GetViewerCount(did) == 8 }, 5*time.Second, 10*time.Millisecond)
	for _, stream := range []string{"did:plc:foreign", "did:plc:unknown", "did:plc:preview", "did:plc:stale"} {
		require.Zero(t, b.GetViewerCount(stream), "relay stats must not claim another origin or an unpublished stream: %s", stream)
	}
	oldForeign := r.originViewForRow(statedb.BroadcastOrigin{
		StreamerRepoDID: did, ServerDID: "did:web:other.example", UpdatedAt: time.Now().Add(-time.Hour),
	})
	require.NoError(t, r.handleOriginMessage(ctx, oldForeign))
	unrelated := r.originViewForRow(statedb.BroadcastOrigin{
		StreamerRepoDID: "did:plc:never-local", ServerDID: "did:web:other.example", UpdatedAt: time.Now(),
	})
	require.NoError(t, r.handleOriginMessage(ctx, unrelated))
	_, remembered := r.latestFor("did:plc:never-local")
	require.False(t, remembered, "disallowed global announcements must not accumulate in the origin cache")
	started, before := time.Now(), queries.Load()
	for count := 6; count <= 9; count++ {
		publish(0, count)
		require.Eventually(t, func() bool { return b.GetViewerCount(did) == count+3 }, 5*time.Second, 10*time.Millisecond)
	}
	require.LessOrEqual(t, queries.Load()-before, int64(time.Since(started)/originPollInterval)+1,
		"stats updates must not scan the origin database on every frame")
	// An external node's origin announcement does not write our local database.
	require.NoError(t, r.handleOriginMessage(ctx, r.originViewForRow(statedb.BroadcastOrigin{
		StreamerRepoDID: did, ServerDID: "did:web:other.example", UpdatedAt: time.Now(),
	})))
	require.False(t, r.hasConnection(did), "remembering ownership must not start a disallowed pull")
	require.Eventually(t, func() bool { return b.GetViewerCount(did) == 0 }, 15*time.Second, 10*time.Millisecond,
		"ownership changes must clear totals even when the relay snapshot is unchanged")
	stops[1]()
	require.Eventually(t, func() bool { return b.GetViewerCount(did) == 1 }, 5*time.Second, 10*time.Millisecond)
}

func TestMoqStatsOverQUIC(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cert, err := moq.NewSelfSignedCert()
	require.NoError(t, err)
	tls, err := moq.ManagedTLSFiles(t.TempDir(), cert.Certificate)
	require.NoError(t, err)
	require.NoError(t, tls.Sync())
	origin := official.NewOriginProducer()
	// Relays scope the wire origin to the authorized URL root (cluster.subscriber).
	// The standalone listener serves that already-scoped origin here.
	broadcast, err := origin.CreateBroadcast("")
	require.NoError(t, err)
	zero := uint32(0)
	track, err := broadcast.PublishJSONSnapshot("viewers/publisher.json", official.JSONSnapshotOptions{DeltaRatio: &zero})
	require.NoError(t, err)
	require.NoError(t, track.Update(map[string]any{"tenant/playback/did:plc:test": map[string]int{"broadcasts_started": 7, "broadcasts_ended": 2}}))
	require.NoError(t, broadcast.Announce(official.Route{}))
	server, err := official.Listen(ctx, "127.0.0.1:0", official.WithTLSCert(tls.CertPath), official.WithTLSKey(tls.KeyPath), official.WithServerPublishOrigin(origin))
	require.NoError(t, err)
	defer server.Close()
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-served; err != nil {
			require.ErrorIs(t, err, context.Canceled)
		}
	}()
	r := &WebsocketReplicator{}
	updates := make(chan map[string]int, 4)
	done := make(chan error, 1)
	go func() {
		done <- r.watchMoqStats(ctx, fmt.Sprintf("moqt://%s/.stats/node/sjc?certhash=%s", server.LocalAddr(), cert.Hash()), "viewers/publisher.json", "tenant/playback/", func(counts map[string]int) { updates <- counts })
	}()
	select {
	case got := <-updates:
		require.Equal(t, map[string]int{"did:plc:test": 5}, got)
	case err := <-done:
		t.Fatalf("stats closed before first snapshot: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.NoError(t, track.Update(map[string]any{}))
	select {
	case got := <-updates:
		require.Empty(t, got, "an empty snapshot clears the prior viewers")
	case err := <-done:
		t.Fatalf("stats closed before update: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.NoError(t, track.Finish())
	select {
	case err := <-done:
		require.Error(t, err, "a closed stats track must trigger fallback")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
