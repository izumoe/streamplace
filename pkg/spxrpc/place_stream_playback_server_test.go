package spxrpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/statedb"
)

func TestPlaybackServerMoqDiscovery(t *testing.T) {
	const did = "did:plc:streamer"
	const moqURL = "moqt://origin.example:38443?certhash=abc123"
	for _, tc := range []struct {
		name   string
		stream string
		url    string
		age    time.Duration
		want   bool
	}{
		{name: "active origin", stream: did, url: moqURL, want: true},
		{name: "resolved alias", stream: "streamer", url: moqURL, want: true},
		{name: "origin without moq", stream: did},
		{name: "stale origin", stream: did, url: moqURL, age: 2 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := statedb.MakeDB(ctx, &config.CLI{DBURL: ":memory:"}, nil, nil)
			require.NoError(t, err)
			require.NoError(t, db.UpsertBroadcastOrigin(did, "did:web:origin.example", tc.url, time.Now().Add(-tc.age)))
			s := &Server{
				cli:        &config.CLI{ServerHost: "playback.example"},
				statefulDB: db,
				aliases:    map[string]string{"streamer": did},
			}
			out, err := s.handlePlaceStreamPlaybackGetPlaybackServer(ctx, tc.stream)
			require.NoError(t, err)
			encoded, err := json.Marshal(out)
			require.NoError(t, err)
			var response struct {
				Servers []string          `json:"servers"`
				Moq     map[string]string `json:"moq"`
			}
			require.NoError(t, json.Unmarshal(encoded, &response))
			require.Equal(t, []string{"https://playback.example"}, response.Servers)
			if tc.want {
				require.Equal(t, map[string]string{"url": moqURL, "name": "playback/" + did}, response.Moq)
			} else {
				require.Nil(t, response.Moq)
			}
		})
	}
}

func TestPlaybackServerMoqDiscoveryTracksOriginChanges(t *testing.T) {
	ctx := context.Background()
	db, err := statedb.MakeDB(ctx, &config.CLI{DBURL: ":memory:"}, nil, nil)
	require.NoError(t, err)
	s := &Server{cli: &config.CLI{ServerHost: "playback.example"}, statefulDB: db}
	const did = "did:plc:streamer"
	for _, moqURL := range []string{"", "moqt://origin.example:38443?certhash=first", "moqt://origin.example:38443?certhash=rotated"} {
		if moqURL != "" {
			require.NoError(t, db.UpsertBroadcastOrigin(did, "did:web:origin.example", moqURL, time.Now()))
		}
		out, err := s.handlePlaceStreamPlaybackGetPlaybackServer(ctx, did)
		require.NoError(t, err)
		encoded, err := json.Marshal(out)
		require.NoError(t, err)
		if moqURL == "" {
			require.NotContains(t, string(encoded), `"moq"`)
		} else {
			require.Contains(t, string(encoded), moqURL)
		}
	}
}

func TestPlaybackServerRequiresStream(t *testing.T) {
	s := &Server{cli: &config.CLI{ServerHost: "playback.example"}}
	_, err := s.handlePlaceStreamPlaybackGetPlaybackServer(context.Background(), "")
	require.Error(t, err)
}
