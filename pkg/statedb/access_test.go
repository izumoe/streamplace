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
		require.Equal(t, uint64(3), revision.Revision)
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

func TestAccessExpiryRetryUsesDatabasePrecision(t *testing.T) {
	WithAllDatabases(t, func(state *StatefulDB) {
		expires := time.Date(2027, time.January, 1, 0, 0, 0, 123456789, time.UTC)
		entry := access.Entry{Authority: "did:web:node.test", Subject: "did:plc:alice", Capability: access.Chat, Effect: access.Deny, CreatedAt: time.Now().UTC(), ExpiresAt: &expires}
		first, err := state.CreateAccessEntry(t.Context(), entry)
		require.NoError(t, err)
		again, err := state.CreateAccessEntry(t.Context(), entry)
		require.NoError(t, err, "retrying the same RFC3339 timestamp must not conflict after a database round trip")
		require.Equal(t, first.ID, again.ID)
		require.Equal(t, 123456789, expires.Nanosecond(), "persisting must not mutate the caller's timestamp")
	})
}

func TestAccessModesAreScopedVersionedAndResettable(t *testing.T) {
	WithAllDatabases(t, func(state *StatefulDB) {
		ctx := t.Context()
		const authority = "did:web:policy.test"
		open, off := access.Open, access.Off
		require.NoError(t, state.SetAccessMode(ctx, authority, access.Streamer, &open, 0, access.Allowlist))
		policy, _, _, err := state.ReadAccess(ctx, authority)
		require.NoError(t, err)
		require.Equal(t, uint64(1), policy.Revision)
		require.Equal(t, access.Open, policy.Modes[access.Streamer])
		require.Zero(t, policy.PublisherGeneration)
		require.NoError(t, state.SetAccessMode(ctx, authority, access.Streamer, &off, 1, access.Allowlist))
		require.NoError(t, state.SetAccessMode(ctx, authority, access.Streamer, &off, 1, access.Allowlist), "identical retries do not need another revision")
		require.ErrorIs(t, state.SetAccessMode(ctx, authority, access.Streamer, &open, 1, access.Allowlist), access.ErrConflict)
		policy, _, _, err = state.ReadAccess(ctx, authority)
		require.NoError(t, err)
		require.Equal(t, uint64(2), policy.Revision)
		require.Equal(t, access.Off, policy.Modes[access.Streamer])
		require.Equal(t, uint64(1), policy.PublisherGeneration)
		require.NoError(t, state.SetAccessMode(ctx, authority, access.Streamer, nil, 2, access.Allowlist))
		policy, _, _, err = state.ReadAccess(ctx, authority)
		require.NoError(t, err)
		require.NotContains(t, policy.Modes, access.Streamer)
		require.Equal(t, uint64(3), policy.Revision)
		require.Equal(t, uint64(1), policy.PublisherGeneration, "resetting policy must not revive revoked admissions")
		other, _, _, err := state.ReadAccess(ctx, "did:web:other.test")
		require.NoError(t, err)
		require.Empty(t, other.Modes)
		for _, capability := range []access.Capability{access.Admin, access.Viewer, "unknown"} {
			require.ErrorIs(t, state.SetAccessMode(ctx, authority, capability, &open, 3, access.Open), access.ErrInvalid)
		}
		invalid := access.Mode("invalid")
		require.ErrorIs(t, state.SetAccessMode(ctx, authority, access.Chat, &invalid, 3, access.Open), access.ErrInvalid)
	})
}
