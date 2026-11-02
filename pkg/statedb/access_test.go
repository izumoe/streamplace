package statedb

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/access"
)

func TestAccessEntriesRemainIndependent(t *testing.T) {
	WithAllDatabases(t, func(state *StatefulDB) {
		ctx := context.Background()
		entry := access.Entry{Authority: "did:web:node.test", Subject: "did:plc:alice", Capability: access.Chat, Effect: access.Allow, CreatedAt: time.Now().UTC()}
		allow, err := state.CreateAccessEntry(ctx, entry)
		require.NoError(t, err)
		again, err := state.CreateAccessEntry(ctx, entry)
		require.NoError(t, err)
		require.Equal(t, allow.ID, again.ID)
		entry.Reason = "different metadata"
		_, err = state.CreateAccessEntry(ctx, entry)
		require.ErrorIs(t, err, access.ErrConflict)
		entry.Effect = access.Deny
		deny, err := state.CreateAccessEntry(ctx, entry)
		require.NoError(t, err)
		require.NoError(t, state.DeleteAccessEntry(ctx, entry.Authority, deny.ID))
		revision, entries, _, err := state.ReadAccess(ctx, entry.Authority)
		require.NoError(t, err)
		require.Equal(t, uint64(3), revision)
		require.Len(t, entries, 1)
		require.Equal(t, allow.ID, entries[0].ID)
		require.ErrorIs(t, state.DeleteAccessEntry(ctx, "did:web:other.test", allow.ID), access.ErrNotFound)
		require.ErrorIs(t, state.DeleteAccessEntry(ctx, entry.Authority, deny.ID), access.ErrNotFound)
	})
}

func TestPublisherGenerationSurvivesRestrictionRemoval(t *testing.T) {
	WithAllDatabases(t, func(state *StatefulDB) {
		ctx := context.Background()
		entry := access.Entry{Authority: "did:web:node.test", Subject: "did:plc:alice", Capability: access.Streamer, Effect: access.Allow, CreatedAt: time.Now().UTC()}
		allow, err := state.CreateAccessEntry(ctx, entry)
		require.NoError(t, err)
		_, _, before, err := state.ReadAccess(ctx, entry.Authority)
		require.NoError(t, err)
		require.NoError(t, state.DeleteAccessEntry(ctx, entry.Authority, allow.ID))
		entry.Effect = access.Deny
		deny, err := state.CreateAccessEntry(ctx, entry)
		require.NoError(t, err)
		require.NoError(t, state.DeleteAccessEntry(ctx, entry.Authority, deny.ID))
		_, entries, after, err := state.ReadAccess(ctx, entry.Authority)
		require.NoError(t, err)
		require.Empty(t, entries)
		require.Greater(t, after[entry.Subject], before[entry.Subject])
	})
}
