package config

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	urfavecli "github.com/urfave/cli/v3"
)

func TestTranscoderURL(t *testing.T) {
	t.Setenv("SP_TRANSCODER_URL", "http://127.0.0.1:8935/transcode")
	cli := flagRun(t)
	require.Equal(t, "http://127.0.0.1:8935/transcode", cli.TranscoderURL)
	require.NoError(t, cli.CheckConfig())
	cli.TranscoderURL = "file:///tmp/transcoder"
	require.ErrorContains(t, cli.CheckConfig(), "transcoder-url")
	cli.TranscoderURL = "https://"
	require.ErrorContains(t, cli.CheckConfig(), "transcoder-url")
}

func TestRetiredServiceFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--livepeer-gateway"},
		{"--livepeer-gateway-url", "http://localhost:8935"},
		{"--livepeer-help"},
		{"--livepeer-debug"},
		{"--livepeer.network", "offchain"},
		{"--beta-invite-did", "did:plc:issuer"},
	} {
		t.Run(args[0], func(t *testing.T) {
			cli := &CLI{}
			cmd := cli.NewCommand("node")
			cmd.Writer, cmd.ErrWriter = io.Discard, io.Discard
			cmd.Action = func(context.Context, *urfavecli.Command) error { return nil }
			require.ErrorContains(t, cmd.Run(context.Background(), append([]string{"node"}, args...)), "flag provided but not defined")
		})
	}
}
