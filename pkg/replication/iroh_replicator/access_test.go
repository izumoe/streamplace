package iroh_replicator

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/media"
	"testing"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
)

func TestOriginsUseCarriagePolicy(t *testing.T) {
	const did = "did:plc:remote-creator"
	for _, localPublisher := range []bool{false, true} {
		cli := &config.CLI{AllowedStreams: []string{"did:plc:local-creator"}}
		if localPublisher {
			cli.AllowedStreams = []string{did}
		} else {
			cli.Syndicate = []string{did}
		}
		swarm := &IrohSwarm{cli: cli, activeSubs: map[string]*SwarmOriginInfo{did: {NodeID: "current-node"}}}
		err := swarm.checkOrigins(t.Context(), did, "current-node")
		if localPublisher {
			require.Error(t, err, "local publishing approval does not grant remote carriage")
		} else {
			require.NoError(t, err, "an admitted remote origin does not need local publishing approval")
		}
	}
}

func TestLocalOriginDoesNotNeedCarriagePermission(t *testing.T) {
	swarm := &IrohSwarm{cli: &config.CLI{}, NodeID: "own-node"}
	require.NoError(t, swarm.checkOrigins(t.Context(), "did:plc:local-creator", "own-node"))
}

func TestDeniedOriginUnsubscribesExistingCarriage(t *testing.T) {
	const did = "did:plc:remote-creator"
	cli := &config.CLI{Syndicate: []string{did}, DisableIrohRelay: true}
	mm := media.NewOffline(cli)
	swarm, err := NewSwarm(t.Context(), cli, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), mm, bus.NewBus(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, swarm.Node.Shutdown()) })
	remote := hex.EncodeToString(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, 32)).Public().(ed25519.PublicKey))
	require.NoError(t, swarm.checkOrigins(t.Context(), did, remote))
	require.Contains(t, swarm.activeSubs, did)
	cli.DisableSyndication = true
	require.ErrorContains(t, swarm.checkOrigins(t.Context(), did, remote), "does not syndicate")
	require.NotContains(t, swarm.activeSubs, did, "a denied origin must be unsubscribed, not retained for repeated validation failures")
}
