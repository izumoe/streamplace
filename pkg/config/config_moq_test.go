package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMoqRelayConfiguration(t *testing.T) {
	for _, relay := range []string{"https://relay.example/anon/node", "moqt://relay.example:443/anon/node"} {
		require.NoError(t, (&CLI{DataDir: t.TempDir(), MoqRelayURL: relay}).CheckConfig())
	}
	for _, relay := range []string{"relay.example", "http://relay.example/anon", "https:///anon", "https://relay.example/anon#fragment", "https://user:secret@relay.example/anon", "https://relay.example/anon?jwt=publish-secret"} {
		err := (&CLI{DataDir: t.TempDir(), MoqRelayURL: relay}).CheckConfig()
		require.ErrorContains(t, err, "--moq-relay-url")
	}
	require.ErrorContains(t, (&CLI{DataDir: t.TempDir(), MoqRelayURL: "https://relay.example/anon", DisableSyndication: true}).CheckConfig(), "--disable-syndication")
}

func TestMoqRelayRequiresWebsocketReplicator(t *testing.T) {
	cli := CLI{DataDir: t.TempDir(), MoqRelayURL: "https://relay.example/anon/node", Replicators: []string{ReplicatorIroh}}
	require.ErrorContains(t, cli.CheckConfig(), "websocket replicator")
}
