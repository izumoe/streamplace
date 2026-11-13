package director

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/accessctl"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/placestream"
	"stream.place/streamplace/pkg/statedb"
)

func TestPublisherRevocationWhilePlaybackWaits(t *testing.T) {
	const did = "did:plc:queued-playback"
	cli := &config.CLI{BroadcasterHost: "node.test", DBURL: ":memory:"}
	state, err := statedb.MakeDB(t.Context(), cli, nil, nil)
	require.NoError(t, err)
	db, err := state.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	b := bus.NewBus()
	controller, err := accessctl.New(t.Context(), access.Bootstrap{
		Authority: "did:web:node.test",
		Entries:   []access.Entry{{Subject: did, Capability: access.Streamer, Effect: access.Allow}},
	}, state, b)
	require.NoError(t, err)
	mm := media.NewOffline(cli)
	mm.Access = controller
	ctx, err := mm.PublisherContext(t.Context(), did, controller.Check(did, access.Streamer).Generation)
	require.NoError(t, err)
	ss := &StreamSession{mm: mm, cli: cli, bus: b}
	l := newLane()
	first, next := l.turn(), l.turn()
	defer first.release()
	done := make(chan error, 1)
	go func() {
		done <- ss.AddToWebRTC(context.WithoutCancel(ctx), &placestream.Segment{Creator: did}, "source", &bus.Seg{Published: true, PlaybackPublished: true}, next)
	}()
	require.Eventually(t, func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.waiters[next.ticket] != nil
	}, time.Second, time.Millisecond)
	denial, err := controller.Create(t.Context(), did, access.Streamer, access.Deny, "revoked", nil)
	require.NoError(t, err)
	require.NoError(t, controller.Delete(t.Context(), denial.ID))
	first.release()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "reconnect")
	case <-time.After(time.Second):
		t.Fatal("queued playback did not finish")
	}
	require.False(t, b.HasSegments(did, "source"), "reapproval cannot release a revoked session's queued segment")
}
