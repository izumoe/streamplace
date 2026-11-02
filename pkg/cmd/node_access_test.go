package cmd

import (
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/accessctl"
	"stream.place/streamplace/pkg/atproto"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/statedb"
)

func TestNodeAccessBootstrapAdmitsOnlyConfiguredTestSigner(t *testing.T) {
	signer, err := ethcrypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)
	key, err := atproto.ParsePubKey(signer.Public())
	require.NoError(t, err)
	otherSigner, err := ethcrypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000002")
	require.NoError(t, err)
	otherKey, err := atproto.ParsePubKey(otherSigner.Public())
	require.NoError(t, err)
	for _, test := range []struct {
		name       string
		testStream bool
		allowed    []string
		mode       string
		want       bool
	}{
		{name: "disabled"},
		{name: "empty allowlist", testStream: true, want: true},
		{name: "configured allowlist", testStream: true, allowed: []string{"did:plc:approved"}, want: true},
		{name: "explicit off", testStream: true, mode: "streamer=off"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cli := &config.CLI{BroadcasterHost: "node.test", DBURL: ":memory:", TestStream: test.testStream, AllowedStreams: test.allowed, AccessPolicy: test.mode}
			bootstrap, err := nodeAccessBootstrap(cli, signer)
			require.NoError(t, err)
			state, err := statedb.MakeDB(t.Context(), cli, nil, nil)
			require.NoError(t, err)
			db, err := state.DB.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			controller, err := accessctl.New(t.Context(), bootstrap, state, nil)
			require.NoError(t, err)
			require.Equal(t, test.want, controller.Check(key.DIDKey(), access.Streamer).Allowed)
			require.False(t, controller.Check(otherKey.DIDKey(), access.Streamer).Allowed)
			require.False(t, controller.Check("did:plc:unrelated", access.Streamer).Allowed)
			require.Equal(t, len(test.allowed) == 0, controller.Check("did:plc:unrelated", access.VOD).Allowed, "test publishing does not change VOD eligibility")
			if len(test.allowed) > 0 {
				require.True(t, controller.Check(test.allowed[0], access.Streamer).Allowed)
			}
		})
	}
}
