package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/access"
)

func TestAccessBootstrap(t *testing.T) {
	cli := CLI{BroadcasterHost: "node.example", AdminDIDs: []string{"did:plc:admin"}}
	bootstrap, err := cli.AccessBootstrap()
	require.NoError(t, err)
	policy, err := access.NewSnapshot(bootstrap, 0, nil, nil)
	require.NoError(t, err)
	require.False(t, policy.Check("did:plc:alice", access.Streamer).Allowed)
	require.True(t, policy.Check("did:plc:alice", access.VOD).Allowed, "bootstrap preserves existing upload eligibility independently")
	require.True(t, policy.Check("did:plc:admin", access.Admin).Allowed)
	require.False(t, policy.Check("did:plc:admin", access.Streamer).Allowed)

	cli.AllowedStreams = []string{"did:plc:alice", "did:key:zTest"}
	cli.TestStream = true
	cli.Syndicate = []string{"did:plc:bob"}
	bootstrap, err = cli.AccessBootstrap()
	require.NoError(t, err)
	policy, err = access.NewSnapshot(bootstrap, 0, nil, nil)
	require.NoError(t, err)
	require.True(t, policy.Check("did:plc:alice", access.Streamer).Allowed)
	require.True(t, policy.Check("did:key:zTest", access.Streamer).Allowed)
	require.False(t, policy.Check("did:plc:bob", access.Streamer).Allowed)
	require.True(t, policy.Check("did:plc:bob", access.Syndicate).Allowed)
	require.False(t, policy.Check("did:plc:alice", access.Syndicate).Allowed)
	require.True(t, policy.Check("did:plc:alice", access.VOD).Allowed)
	require.False(t, policy.Check("did:plc:bob", access.VOD).Allowed)
}

func TestAccessModesAreExplicit(t *testing.T) {
	for _, invalid := range []string{"streamer", "streamer=oops", "unknown=open", "admin=open", "viewer=allowlist"} {
		_, err := (&CLI{AccessPolicy: invalid}).AccessBootstrap()
		require.Error(t, err, invalid)
	}
	bootstrap, err := (&CLI{AccessPolicy: "streamer=open,vod=off"}).AccessBootstrap()
	require.NoError(t, err)
	policy, err := access.NewSnapshot(bootstrap, 0, nil, nil)
	require.NoError(t, err)
	require.True(t, policy.Check("did:plc:alice", access.Streamer).Allowed)
	require.False(t, policy.Check("did:key:zTest", access.Streamer).Allowed)
	require.False(t, policy.Check("did:plc:alice", access.VOD).Allowed)
}

func TestSyndicationWildcardDoesNotDependOnListOrder(t *testing.T) {
	for _, list := range [][]string{{"*", "did:plc:bob"}, {"did:plc:bob", "*"}} {
		cli := CLI{Syndicate: list}
		require.True(t, cli.CapabilityAllowed("did:plc:alice", access.Syndicate))
		cli.DisableSyndication = true
		require.False(t, cli.CapabilityAllowed("did:plc:alice", access.Syndicate))
	}
}

func TestSyndicationUsesExplicitCapabilityMode(t *testing.T) {
	require.True(t, (&CLI{AccessPolicy: "syndicate=open"}).ShouldSyndicate("did:plc:alice"))
	require.False(t, (&CLI{AccessPolicy: "syndicate=off", Syndicate: []string{"*"}}).ShouldSyndicate("did:plc:alice"))
}
