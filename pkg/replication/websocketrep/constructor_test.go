package websocketrep

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/moq"
	"stream.place/streamplace/pkg/placestream"
)

func TestOriginRecordHasNodeConfigurationBeforeStart(t *testing.T) {
	cert, err := moq.NewSelfSignedCert()
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		cli  *config.CLI
		url  string
	}{
		{"listener", &config.CLI{ServerHost: "node.example", MoqAddr: ":443"}, "moqt://node.example:443?certhash=" + cert.Hash()},
		{"relay", &config.CLI{ServerHost: "node.example", MoqRelayURL: "https://relay.example/anon/node"}, "https://relay.example/anon/node"},
		{"disabled", &config.CLI{ServerHost: "node.example"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewWebsocketReplicator(tc.cli, bus.NewBus(), nil, nil, nil, cert)
			origin := &placestream.BroadcastOrigin{Streamer: "did:plc:streamer"}
			require.NotPanics(t, func() { require.NoError(t, r.BuildOriginRecord(origin)) })
			require.NotNil(t, origin.WebsocketURL)
			require.Contains(t, *origin.WebsocketURL, "ws://"+tc.cli.ServerHost+"/")
			if tc.url == "" {
				require.Nil(t, origin.MoqURL)
			} else {
				require.Equal(t, tc.url, *origin.MoqURL)
			}
		})
	}
}

func TestStartKeepsConstructorConfigurationDuringDiscovery(t *testing.T) {
	cli := &config.CLI{ServerHost: "configured.example"}
	mod, err := model.MakeDB(":memory:")
	require.NoError(t, err)
	db, err := mod.(*model.DBModel).DB.DB()
	require.NoError(t, err)
	defer db.Close()
	r := NewWebsocketReplicator(cli, bus.NewBus(), mod, nil, nil, nil)
	stopped, cancel := context.WithCancel(t.Context())
	cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() { close(started); done <- r.Start(stopped, &config.CLI{ServerHost: "other.example"}) }()
	<-started
	for range 1000 {
		origin := &placestream.BroadcastOrigin{Streamer: "did:plc:streamer"}
		require.NoError(t, r.BuildOriginRecord(origin))
		require.Contains(t, *origin.WebsocketURL, "ws://configured.example/")
	}
	require.ErrorIs(t, <-done, context.Canceled)
}
