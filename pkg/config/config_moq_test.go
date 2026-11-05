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

func TestMoqSessionLimitConfiguration(t *testing.T) {
	defaults := flagRun(t)
	require.Equal(t, 20, defaults.MoqSessionRateLimit)
	require.Equal(t, 100, defaults.MoqSessionRateBurst)
	require.Empty(t, defaults.MoqSessionTrustedProxies)
	custom := flagRun(t, "--moq-session-rate-limit", "40", "--moq-session-rate-burst", "200", "--moq-session-trusted-proxies", "127.0.0.1/32", "--moq-session-trusted-proxies", "::1/128")
	require.Equal(t, 40, custom.MoqSessionRateLimit)
	require.Equal(t, 200, custom.MoqSessionRateBurst)
	require.Equal(t, []string{"127.0.0.1/32", "::1/128"}, custom.MoqSessionTrustedProxies)
	for _, cli := range []*CLI{
		{MoqSessionRateLimit: -1},
		{MoqSessionRateLimit: 20, MoqSessionRateBurst: 0},
		{MoqSessionRateLimit: 20, MoqSessionRateBurst: -1},
		{MoqSessionTrustedProxies: []string{"not-a-cidr"}},
	} {
		cli.DataDir = t.TempDir()
		require.ErrorContains(t, cli.CheckConfig(), "--moq-session-")
	}
	t.Setenv("SP_MOQ_SESSION_RATE_LIMIT", "0")
	t.Setenv("SP_MOQ_SESSION_RATE_BURST", "250")
	t.Setenv("SP_MOQ_SESSION_TRUSTED_PROXIES", "10.0.0.0/24,::1/128")
	fromEnv := flagRun(t)
	require.Zero(t, fromEnv.MoqSessionRateLimit, "an operator can explicitly disable the limiter")
	require.Equal(t, 250, fromEnv.MoqSessionRateBurst)
	require.Equal(t, []string{"10.0.0.0/24", "::1/128"}, fromEnv.MoqSessionTrustedProxies)
}
