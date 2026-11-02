package iroh_replicator

import (
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
