package accessctl

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/statedb"
)

func TestRestrictionReapprovalAndReload(t *testing.T) {
	ctx := context.Background()
	state, err := statedb.MakeDB(ctx, &config.CLI{DBURL: ":memory:"}, nil, nil)
	require.NoError(t, err)
	db, err := state.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	bootstrap := access.Bootstrap{Authority: "did:web:node.test", Modes: map[access.Capability]access.Mode{access.Streamer: access.Open}}
	b := bus.NewBus()
	controller, err := New(ctx, bootstrap, state, b)
	require.NoError(t, err)
	before := controller.Check("did:plc:alice", access.Streamer)
	require.True(t, before.Allowed)
	deny, err := controller.Create(ctx, "did:plc:alice", access.Streamer, access.Deny, "restricted", nil)
	require.NoError(t, err)
	require.False(t, controller.Check("did:plc:alice", access.Streamer).Allowed)
	require.NoError(t, controller.Delete(ctx, deny.ID))
	after := controller.Check("did:plc:alice", access.Streamer)
	require.True(t, after.Allowed)
	require.Greater(t, after.Generation, before.Generation)
	reloaded, err := New(ctx, bootstrap, state, b)
	require.NoError(t, err)
	require.Equal(t, after, reloaded.Check("did:plc:alice", access.Streamer))
	_, err = controller.Create(ctx, "did:plc:alice", access.Admin, access.Allow, "", nil)
	require.ErrorIs(t, err, access.ErrInvalid)
	past := time.Now().Add(-time.Second)
	_, err = controller.Create(ctx, "did:plc:alice", access.Chat, access.Deny, "", &past)
	require.ErrorIs(t, err, access.ErrInvalid)
}

func TestSharedDatabaseRefreshAndConcurrentIdempotency(t *testing.T) {
	ctx := t.Context()
	config := &config.CLI{DBURL: "sqlite://" + t.TempDir() + "/access.db"}
	state, err := statedb.MakeDB(ctx, config, nil, nil)
	require.NoError(t, err)
	db, err := state.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	bootstrap := access.Bootstrap{Authority: "did:web:node.test"}
	first, err := New(ctx, bootstrap, state, nil)
	require.NoError(t, err)
	secondState, err := statedb.MakeDB(ctx, config, nil, nil)
	require.NoError(t, err)
	secondDB, err := secondState.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, secondDB.Close()) })
	second, err := New(ctx, bootstrap, secondState, nil)
	require.NoError(t, err)
	results := make(chan access.Entry, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		ctl := first
		if i%2 == 1 {
			ctl = second
		}
		go func() {
			entry, err := ctl.Create(ctx, "did:plc:alice", access.Streamer, access.Allow, "", nil)
			results <- entry
			failures <- err
		}()
	}
	var id string
	for i := 0; i < 8; i++ {
		entry := <-results
		require.NoError(t, <-failures)
		if id == "" {
			id = entry.ID
		}
		require.Equal(t, id, entry.ID)
	}
	require.NoError(t, first.Refresh(ctx))
	require.NoError(t, second.Refresh(ctx))
	require.True(t, second.Check("did:plc:alice", access.Streamer).Allowed)
	snapshot := second.Snapshot()
	require.NoError(t, first.Delete(ctx, id))
	require.NoError(t, second.Refresh(ctx))
	require.False(t, second.Check("did:plc:alice", access.Streamer).Allowed)
	require.True(t, snapshot.Check("did:plc:alice", access.Streamer).Allowed, "previous immutable snapshot must remain stable")
}

func TestChatRestrictionExpiryRefreshesWithoutNewRevision(t *testing.T) {
	state, err := statedb.MakeDB(t.Context(), &config.CLI{DBURL: ":memory:"}, nil, nil)
	require.NoError(t, err)
	db, err := state.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	b := bus.NewBus()
	ctl, err := New(t.Context(), access.Bootstrap{Authority: "did:web:node.test"}, state, b)
	require.NoError(t, err)
	changes := b.Subscribe(access.Topic)
	t.Cleanup(func() { b.Unsubscribe(access.Topic, changes) })
	expires := time.Now().Add(100 * time.Millisecond)
	_, err = ctl.Create(t.Context(), "did:plc:alice", access.Chat, access.Deny, "temporary", &expires)
	require.NoError(t, err)
	select {
	case change := <-changes:
		require.True(t, change.(access.Change).ChatChanged)
	case <-time.After(time.Second):
		t.Fatal("missing restriction notification")
	}
	revision := ctl.Snapshot().Revision()
	require.Eventually(t, func() bool { return ctl.Check("did:plc:alice", access.Chat).Allowed }, time.Second, time.Millisecond)
	require.NoError(t, ctl.Refresh(t.Context()))
	select {
	case message := <-changes:
		change := message.(access.Change)
		require.True(t, change.ChatChanged)
		require.Equal(t, revision, change.Revision, "expiry changes the decision without a database write")
	case <-time.After(time.Second):
		t.Fatal("missing expiry notification")
	}
}

func TestPublisherMutationDoesNotRefreshUnchangedChat(t *testing.T) {
	state, err := statedb.MakeDB(t.Context(), &config.CLI{DBURL: ":memory:"}, nil, nil)
	require.NoError(t, err)
	db, err := state.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	b := bus.NewBus()
	ctl, err := New(t.Context(), access.Bootstrap{Authority: "did:web:node.test"}, state, b)
	require.NoError(t, err)
	changes := b.Subscribe(access.Topic)
	t.Cleanup(func() { b.Unsubscribe(access.Topic, changes) })
	_, err = ctl.Create(t.Context(), "did:plc:alice", access.Chat, access.Deny, "", nil)
	require.NoError(t, err)
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("missing chat change")
	}
	_, err = ctl.Create(t.Context(), "did:plc:bob", access.Streamer, access.Allow, "", nil)
	require.NoError(t, err)
	select {
	case change := <-changes:
		t.Fatalf("unrelated publisher allowance refreshed chat: %+v", change)
	case <-time.After(20 * time.Millisecond):
	}
}
