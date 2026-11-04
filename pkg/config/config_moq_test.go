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

func TestMoqStatsConfiguration(t *testing.T) {
	const relay = "https://relay.example/tenant"
	stats := "https://relay.example/.stats/node/sjc?jwt=private-stats-access"
	cli := &CLI{DataDir: t.TempDir(), MoqRelayURL: relay, MoqStatsURLs: []string{stats}, MoqStatsTier: "viewer"}
	require.NoError(t, cli.CheckConfig())
	for _, raw := range []string{"relative", "http://relay.example/.stats/node", "https://user:secret@relay.example/.stats/node", "https://relay.example/.stats/node#fragment"} {
		cli.MoqStatsURLs = []string{raw}
		err := cli.CheckConfig()
		require.ErrorContains(t, err, "--moq-stats-url")
		require.NotContains(t, err.Error(), "secret")
	}
	cli.MoqStatsURLs = []string{stats, stats}
	require.ErrorContains(t, cli.CheckConfig(), "duplicate")
	cli.MoqStatsURLs = []string{stats, "https://relay.example/.stats/node/sjc?jwt=another-token"}
	require.ErrorContains(t, cli.CheckConfig(), "duplicate")
	cli.MoqStatsURLs = []string{stats}
	for _, tier := range []string{"/viewer", "viewer/", "viewer//edge", ".", "viewer/../peer"} {
		cli.MoqStatsTier = tier
		require.ErrorContains(t, cli.CheckConfig(), "--moq-stats-tier")
	}
	cli.MoqStatsTier = "viewer"
	cli.MoqStatsURLs = []string{stats}
	cli.MoqRelayURL = ""
	require.ErrorContains(t, cli.CheckConfig(), "requires --moq-relay-url")
	parsed := flagRun(t, "--moq-relay-url", relay, "--moq-stats-url", stats, "--moq-stats-url", "https://relay.example/.stats/node/lhr", "--moq-stats-tier", "viewer")
	require.Equal(t, []string{stats, "https://relay.example/.stats/node/lhr"}, parsed.MoqStatsURLs)
	require.Equal(t, "viewer", parsed.MoqStatsTier)
}
