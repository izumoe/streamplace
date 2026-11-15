package spxrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/moq"
	"stream.place/streamplace/pkg/muxl"
	"stream.place/streamplace/pkg/replication/websocketrep"
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
				require.Equal(t, map[string]string{"url": moqURL, "name": "playback/" + did, "sessionUrl": "https://origin.example/api/moq/session?streamer=" + url.QueryEscape(did)}, response.Moq)
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

func TestPlaybackServerPrefersOwnMoqForPublicReceivedStems(t *testing.T) {
	const did = "did:plc:received-stems"
	ctx := t.Context()
	input, err := os.ReadFile("../../test/fixtures/h264-opus-frag.mp4")
	require.NoError(t, err)
	av1, err := os.ReadFile("../../test/fixtures/ertmp-green-av1.mp4")
	require.NoError(t, err)
	stem, err := muxl.RunMuxlCanonicalize(ctx, input, map[uint32]uint32{2: 4}, muxl.WithTrackMeta(map[uint32]muxl.TrackMeta{4: {AlternateGroup: 2, Label: "Music"}}))
	require.NoError(t, err)
	events := make(chan *muxl.MuxlEvent, 16)
	done := make(chan error, 1)
	go func() { done <- muxl.RunMuxlUnwrapEvents(ctx, bytes.NewReader(stem), events); close(events) }()
	var stemBytes []byte
	for event := range events {
		stemBytes = append(stemBytes, event.Tracks["4"]...)
	}
	require.NoError(t, <-done)
	require.NotEmpty(t, stemBytes)
	for _, tc := range []struct {
		name                                                                       string
		private, offline, finalized, ungrouped, noListener, av1, noOwner, listener bool
	}{
		{name: "received stems without origin table row", noOwner: true},
		{name: "received stems override upstream owner"},
		{name: "listener certificate is pinned", listener: true},
		{name: "AV1 is independent of WebRTC packetization", av1: true},
		{name: "preview keeps existing discovery", private: true},
		{name: "ended keeps existing discovery", finalized: true},
		{name: "offline keeps existing discovery", offline: true},
		{name: "no local MoQ keeps existing discovery", noListener: true},
		{name: "ordinary stream keeps existing discovery", ungrouped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli := &config.CLI{ServerHost: "receiver.example", MoqRelayURL: "moqt://127.0.0.1:1/receiver"}
			if tc.noListener {
				cli.MoqRelayURL = ""
				cli.MoqURL = "moqt://unused.example"
			}
			cert, err := moq.NewSelfSignedCert()
			require.NoError(t, err)
			expectedOwn := cli.MoqRelayURL
			if tc.listener {
				cli.MoqRelayURL = ""
				cli.MoqAddr = "127.0.0.1:1"
				cli.MoqURL = "moqt://receiver.example:4443"
				expectedOwn = cli.MoqURL + "?certhash=" + cert.Hash()
			}
			mm := media.NewOffline(cli)
			if !tc.offline {
				main := input
				if tc.av1 {
					main = av1
				}
				canonical, err := muxl.RunMuxlCanonicalize(ctx, main, nil)
				require.NoError(t, err)
				require.NoError(t, mm.FeedLiveRenditions(ctx, did, canonical, time.Now(), !tc.private))
				if !tc.ungrouped {
					require.NoError(t, mm.FeedLiveRenditions(ctx, did, stemBytes, time.Now(), !tc.private))
				}
				if tc.finalized {
					window, _ := mm.GetLiveWindow(did)
					require.NotNil(t, window)
					window.Finalize()
				}
			}
			mod, err := model.MakeDB(":memory:")
			require.NoError(t, err)
			replicator := websocketrep.NewWebsocketReplicator(cli, bus.NewBus(), mod, mm, nil, cert)
			db, err := statedb.MakeDB(ctx, &config.CLI{DBURL: ":memory:"}, nil, nil)
			require.NoError(t, err)
			if !tc.noOwner {
				require.NoError(t, db.UpsertBroadcastOrigin(did, "did:web:owner.example", "moqt://owner.example", time.Now()))
			}
			server := &Server{cli: cli, mm: mm, statefulDB: db, Replicator: replicator}
			out, err := server.handlePlaceStreamPlaybackGetPlaybackServer(ctx, did)
			require.NoError(t, err)
			require.NotNil(t, out.Moq)
			local := !tc.private && !tc.offline && !tc.finalized && !tc.ungrouped && !tc.noListener
			if local {
				require.Equal(t, expectedOwn, out.Moq.Url)
				require.Equal(t, "https://receiver.example/api/moq/session?streamer="+url.QueryEscape(did), *out.Moq.SessionUrl)
			} else {
				require.Equal(t, "moqt://owner.example", out.Moq.Url)
				require.Equal(t, "https://owner.example/api/moq/session?streamer="+url.QueryEscape(did), *out.Moq.SessionUrl)
			}
			if !tc.noOwner {
				origin, err := db.GetLatestBroadcastOriginForStreamer(did)
				require.NoError(t, err)
				require.Equal(t, "moqt://owner.example", origin.MoqURL, "discovery must not rewrite the owner's origin")
			}
		})
	}
}
