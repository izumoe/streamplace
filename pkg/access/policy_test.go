package access

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCapabilityDecisions(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	const alice = "did:plc:alice"
	config := Bootstrap{
		Authority: "did:web:node.example",
		Modes:     map[Capability]Mode{Admin: Allowlist, Viewer: Open, Chat: Open, Streamer: Allowlist, Syndicate: Off, VOD: Allowlist},
		Entries:   []Entry{{Subject: alice, Capability: Admin, Effect: Allow, Source: Configuration}},
	}
	policy, err := NewSnapshot(config, 1, nil)
	require.NoError(t, err)
	require.True(t, policy.CheckAt(alice, Admin, now).Allowed)
	require.False(t, policy.CheckAt(alice, Streamer, now).Allowed, "administration does not grant publishing")
	require.True(t, policy.CheckAt("", Viewer, now).Allowed)
	require.True(t, policy.CheckAt("did:key:zTest", Viewer, now).Allowed, "public viewing cannot depend on an identity grant")
	require.False(t, policy.CheckAt("", Chat, now).Allowed, "chat still needs an identified author")
	require.False(t, policy.CheckAt(alice, Capability("unknown"), now).Allowed)
	require.False(t, policy.CheckAt(alice, Syndicate, now).Allowed)

	grant := Entry{ID: "grant", Subject: alice, Capability: Chat, Effect: Allow, Source: Stored}
	ban := Entry{ID: "ban", Subject: alice, Capability: Chat, Effect: Deny, Source: Stored}
	config.Modes[Chat] = Allowlist
	policy, err = NewSnapshot(config, 2, []Entry{grant, ban})
	require.NoError(t, err)
	require.False(t, policy.CheckAt(alice, Chat, now).Allowed)
	policy, err = NewSnapshot(config, 3, []Entry{grant})
	require.NoError(t, err)
	require.True(t, policy.CheckAt(alice, Chat, now).Allowed, "unban retains the independent allowance")
	policy, err = NewSnapshot(config, 4, nil)
	require.NoError(t, err)
	require.False(t, policy.CheckAt(alice, Chat, now).Allowed, "unban does not invent an allowance")
}

func TestRestrictionExpiryAndConfiguration(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	expires := now.Add(time.Minute)
	const alice = "did:plc:alice"
	config := Bootstrap{Authority: "did:web:node.example", Modes: map[Capability]Mode{Streamer: Open}, Entries: []Entry{{Subject: alice, Capability: Streamer, Effect: Allow, Source: Configuration}}}
	policy, err := NewSnapshot(config, 1, []Entry{{Subject: alice, Capability: Streamer, Effect: Deny, ExpiresAt: &expires, Source: Stored}})
	require.NoError(t, err)
	require.False(t, policy.CheckAt(alice, Streamer, now).Allowed, "deny wins over open mode and a configured grant")
	require.True(t, policy.CheckAt(alice, Streamer, expires).Allowed, "expiry is exclusive")
	require.False(t, policy.CheckAt("did:key:zSynthetic", Streamer, expires).Allowed, "open mode does not admit test identities")
	config.Modes[Streamer] = Off
	policy, err = NewSnapshot(config, 2, nil)
	require.NoError(t, err)
	require.False(t, policy.CheckAt(alice, Streamer, now).Allowed, "off wins over an explicit grant")
}

func TestSnapshotOwnsItsInputs(t *testing.T) {
	config := Bootstrap{Authority: "did:web:node.example", Modes: map[Capability]Mode{Streamer: Allowlist}}
	entries := []Entry{{Subject: "did:plc:alice", Capability: Streamer, Effect: Allow, Source: Stored}}
	policy, err := NewSnapshot(config, 3, entries)
	require.NoError(t, err)
	config.Modes[Streamer] = Open
	entries[0].Effect = Deny
	require.True(t, policy.Check("did:plc:alice", Streamer).Allowed)
	require.False(t, policy.Check("did:plc:bob", Streamer).Allowed)
	listed := policy.Entries()
	listed[0].Effect = Deny
	require.True(t, policy.Check("did:plc:alice", Streamer).Allowed)
}

func TestRejectUnsupportedAndMalformedPolicy(t *testing.T) {
	for _, config := range []Bootstrap{
		{Authority: "did:web:node.example", Modes: map[Capability]Mode{Viewer: Allowlist}},
		{Authority: "did:web:node.example", Modes: map[Capability]Mode{Admin: Open}},
		{Authority: "did:web:node.example", Modes: map[Capability]Mode{Capability("unknown"): Open}},
		{Authority: "did:web:node.example", Entries: []Entry{{Subject: "alice.example", Capability: Chat, Effect: Deny}}},
	} {
		_, err := NewSnapshot(config, 0, nil)
		require.Error(t, err)
	}
	for _, entry := range []Entry{
		{Subject: "did:plc:alice", Capability: Viewer, Effect: Deny},
		{Subject: "did:plc:alice", Capability: Admin, Effect: Allow},
		{Subject: "did:plc:alice", Capability: Syndicate, Effect: Deny},
		{Subject: "did:plc:alice", Capability: VOD, Effect: Allow},
	} {
		require.Error(t, ValidateMutation(entry))
	}
	require.NoError(t, ValidateMutation(Entry{Subject: "did:plc:alice", Capability: Chat, Effect: Deny}))
}
