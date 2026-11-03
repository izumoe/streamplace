package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	urfavecli "github.com/urfave/cli/v3"
	"stream.place/streamplace/pkg/config"
)

func TestE2ENodeProfilerEnvironment(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native heap profiling uses Linux LD_PRELOAD")
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			dir := t.TempDir()
			output := filepath.Join(dir, "environment")
			self := filepath.Join(dir, "node")
			require.NoError(t, os.WriteFile(self, []byte("#!/bin/sh\nprintf '%s\\n%s\\n' \"$LD_PRELOAD\" \"$DUMP_HEAPTRACK_OUTPUT\" > \"$E2E_TEST_ENV_PATH\"\n"), 0o700))
			t.Setenv("E2E_TEST_ENV_PATH", output)
			t.Setenv("LD_PRELOAD", "")
			t.Setenv("DUMP_HEAPTRACK_OUTPUT", "inherited-profile")
			t.Setenv("E2E_NODE_LD_PRELOAD", "")
			t.Setenv("E2E_NODE_DUMP_HEAPTRACK_OUTPUT", "")
			require.NoError(t, os.Unsetenv("E2E_NODE_LD_PRELOAD"))
			require.NoError(t, os.Unsetenv("E2E_NODE_DUMP_HEAPTRACK_OUTPUT"))
			if enabled {
				t.Setenv("E2E_NODE_LD_PRELOAD", "libm.so.6")
				t.Setenv("E2E_NODE_DUMP_HEAPTRACK_OUTPUT", filepath.Join(dir, "heaptrack.$$"))
			}
			cmd, err := startNode(t.Context(), self, e2eDevEnv{}, dir, nil)
			require.NoError(t, err)
			require.NoError(t, cmd.Wait())
			data, err := os.ReadFile(output)
			require.NoError(t, err)
			want := "\ninherited-profile\n"
			if enabled {
				want = "libm.so.6\n" + filepath.Join(dir, "heaptrack.$$") + "\n"
			}
			require.Equal(t, want, string(data))
		})
	}
}

func TestE2EPublisherFlags(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		want       bool
		unapproved bool
	}{
		{name: "fixture by default", args: []string{"e2e"}},
		{name: "external publisher", args: []string{"e2e", "--external-stream"}, want: true},
		{name: "explicit fixture", args: []string{"e2e", "--external-stream=false"}},
		{name: "unapproved fixture", args: []string{"e2e", "--unapproved-stream"}, unapproved: true},
		{name: "unapproved external publisher", args: []string{"e2e", "--external-stream", "--unapproved-stream"}, want: true, unapproved: true},
		{name: "explicit approved fixture", args: []string{"e2e", "--unapproved-stream=false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := makeE2eCommand(&config.BuildFlags{})
			cmd.Writer, cmd.ErrWriter = io.Discard, io.Discard
			stop := errors.New("flags parsed")
			cmd.Before = func(_ context.Context, cmd *urfavecli.Command) (context.Context, error) {
				require.Equal(t, tc.want, cmd.Bool("external-stream"))
				require.Equal(t, tc.unapproved, cmd.Bool("unapproved-stream"))
				return nil, stop
			}
			require.ErrorIs(t, cmd.Run(t.Context(), tc.args), stop)
		})
	}
}
