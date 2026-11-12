package director

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bluesky-social/indigo/xrpc"
	slogGorm "github.com/orandin/slog-gorm"
	glex "github.com/streamplace/glex/runtime"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/appbsky"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/comatproto"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/devenv"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/placestream"
	"stream.place/streamplace/pkg/replication/websocketrep"
)

func TestMetadataCreatesMissingRecords(t *testing.T) {
	var logs bytes.Buffer
	logger, gormLogger := slog.Default(), config.GormLogger
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	config.GormLogger = slogGorm.New(slogGorm.WithHandler(slog.Default().Handler()))
	t.Cleanup(func() { slog.SetDefault(logger); config.GormLogger = gormLogger })
	const did = "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa"
	const recordCID = "bafyreigrquyde7kfqjfwdtbgmcf6ugzk6kxeb3k3z55o47hf35ihur6xrq"
	mod, err := model.MakeDB(":memory:")
	require.NoError(t, err)
	db, err := mod.(*model.DBModel).DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	cli := &config.CLI{ServerHost: "node.test", BroadcasterHost: "node.test", WebsocketURL: "wss://node.test", DevAccountCreds: map[string]string{did: "test-password"}}
	replicator := websocketrep.NewWebsocketReplicator(bus.NewBus(), mod, nil, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, replicator.Start(ctx, cli), context.Canceled)
	require.NoError(t, mod.UpdateRepo(&model.Repo{DID: did, Handle: "publisher.test"}))
	var broadcast bytes.Buffer
	require.NoError(t, (&placestream.Livestream{Title: "Test broadcast", CreatedAt: time.Now().UTC().Format(time.RFC3339)}).MarshalCBOR(&broadcast))
	data := broadcast.Bytes()
	require.NoError(t, mod.CreateLivestream(t.Context(), &model.Livestream{URI: "at://" + did + "/place.stream.livestream/test", CID: recordCID, RepoDID: did, CreatedAt: time.Now(), Livestream: &data}))
	for _, kind := range []string{"origin", "status"} {
		t.Run(kind, func(t *testing.T) {
			collection, rkey := "place.stream.broadcast.origin", did+"::did:web:node.test"
			if kind == "status" {
				collection, rkey = "app.bsky.actor.status", "self"
			}
			for _, tc := range []struct {
				name, code  string
				status      int
				write, race bool
			}{
				{"reference PDS missing record", "RecordNotFound", 400, true, false},
				{"mycelial PDS missing record", "RecordNotFound", 404, true, false},
				{"existing record", "", 200, true, false},
				{"invalid request", "InvalidRequest", 400, false, false},
				{"missing endpoint", "MethodNotFound", 404, false, false},
				{"unauthorized", "AuthRequired", 401, false, false},
				{"forbidden", "Forbidden", 403, false, false},
				{"PDS failure", "InternalServerError", 500, false, false},
				{"misreported PDS failure", "RecordNotFound", 500, false, false},
				{"creation race", "RecordNotFound", 400, false, true},
				{"update race", "", 200, false, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					written := make(chan *comatproto.RepoPutRecord_Input, 1)
					pds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						switch r.URL.Path {
						case "/xrpc/com.atproto.server.createSession":
							require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"did": did, "handle": "publisher.test", "accessJwt": "test-access", "refreshJwt": "test-refresh"}))
						case "/xrpc/com.atproto.repo.getRecord":
							require.Equal(t, did, r.URL.Query().Get("repo"))
							require.Equal(t, collection, r.URL.Query().Get("collection"))
							require.Equal(t, rkey, r.URL.Query().Get("rkey"))
							w.WriteHeader(tc.status)
							if tc.status == http.StatusOK {
								var existing any = &placestream.BroadcastOrigin{Streamer: did, Server: "did:web:node.test"}
								if kind == "status" {
									existing = &appbsky.ActorStatus{Status: "app.bsky.actor.status#live"}
								}
								require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"uri": "at://" + did + "/" + collection + "/" + rkey, "cid": recordCID, "value": existing}))
							} else {
								require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"error": tc.code, "message": "Record not found"}))
							}
						case "/xrpc/com.atproto.repo.putRecord":
							stored := &comatproto.RepoPutRecord_Input{}
							data, err := io.ReadAll(r.Body)
							require.NoError(t, err)
							require.NoError(t, json.Unmarshal(data, stored))
							var fields map[string]json.RawMessage
							require.NoError(t, json.Unmarshal(data, &fields))
							if _, guarded := fields["swapRecord"]; tc.race && guarded {
								w.WriteHeader(http.StatusBadRequest)
								require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"error": "InvalidSwap", "message": "Record changed after read"}))
								return
							}
							written <- stored
							require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"uri": "at://" + did + "/" + collection + "/" + rkey, "cid": recordCID}))
						default:
							http.NotFound(w, r)
						}
					}))
					defer pds.Close()
					require.NoError(t, mod.UpdateRepo(&model.Repo{DID: did, Handle: "publisher.test", PDS: pds.URL}))
					ss := &StreamSession{repoDID: did, cli: cli, mod: mod, replicator: replicator}
					var err error
					if kind == "origin" {
						err = ss.doUpdateBroadcastOrigin(t.Context())
					} else {
						err = ss.doUpdateStatus(t.Context(), did)
					}
					updatedAt := ss.lastOriginTime
					if kind == "status" {
						updatedAt = ss.lastStatus
					}
					if !tc.write {
						require.Error(t, err)
						require.Empty(t, written, "an unrelated PDS failure must not create or overwrite a record")
						require.True(t, updatedAt.IsZero())
						if kind == "status" {
							require.Nil(t, ss.lastStatusCID, "failed writes cannot become the status owned by this session")
						}
						return
					}
					require.NoError(t, err)
					require.NotEmpty(t, written)
					stored := <-written
					require.Equal(t, did, stored.Repo)
					require.Equal(t, collection, stored.Collection)
					require.Equal(t, rkey, stored.Rkey)
					if tc.status == http.StatusOK {
						require.NotNil(t, stored.SwapRecord)
						require.Equal(t, recordCID, *stored.SwapRecord)
					} else {
						require.Nil(t, stored.SwapRecord)
					}
					if kind == "status" {
						status, ok := stored.Record.Val.(*appbsky.ActorStatus)
						require.True(t, ok)
						require.Equal(t, "app.bsky.actor.status#live", status.Status)
						require.NotNil(t, status.Embed)
						require.Equal(t, "https://node.test/publisher.test", status.Embed.EmbedExternal.External.Uri)
						require.NotNil(t, ss.lastStatusCID)
						require.Equal(t, recordCID, *ss.lastStatusCID)
					} else {
						origin, ok := stored.Record.Val.(*placestream.BroadcastOrigin)
						require.True(t, ok)
						require.Equal(t, did, origin.Streamer)
						require.Equal(t, "did:web:node.test", origin.Server)
						require.Contains(t, *origin.WebsocketURL, "wss://node.test/xrpc/place.stream.live.subscribeSegments?")
						updated, err := time.Parse(time.RFC3339, origin.UpdatedAt)
						require.NoError(t, err)
						require.WithinDuration(t, time.Now(), updated, 5*time.Second)
					}
					require.False(t, updatedAt.IsZero())
				})
			}
		})
	}
}

func TestConditionalRecordProtectsConcurrentPDSWrites(t *testing.T) {
	dev := devenv.WithDevEnv(t)
	account := dev.CreateAccount(t)
	ctx := t.Context()
	origin := &placestream.BroadcastOrigin{Streamer: account.DID, Server: "did:web:node.test", UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	input := comatproto.RepoPutRecord_Input{Repo: account.DID, Collection: "place.stream.broadcast.origin", Rkey: "self", Record: &glex.LexiconTypeDecoder{Val: origin}}
	var created comatproto.RepoPutRecord_Output
	require.NoError(t, account.XRPC.Do(ctx, xrpc.Procedure, "application/json", "com.atproto.repo.putRecord", nil, conditionalRecord(input), &created))

	origin.UpdatedAt = time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
	var updated comatproto.RepoPutRecord_Output
	err := account.XRPC.Do(ctx, xrpc.Procedure, "application/json", "com.atproto.repo.putRecord", nil, conditionalRecord(input), &updated)
	var conflict *xrpc.XRPCError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "InvalidSwap", conflict.ErrStr, "a competing first writer must not replace an existing record")

	input.SwapRecord = &created.Cid
	require.NoError(t, account.XRPC.Do(ctx, xrpc.Procedure, "application/json", "com.atproto.repo.putRecord", nil, conditionalRecord(input), &updated))
	require.NotEqual(t, created.Cid, updated.Cid)
	origin.UpdatedAt = time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339)
	err = account.XRPC.Do(ctx, xrpc.Procedure, "application/json", "com.atproto.repo.putRecord", nil, conditionalRecord(input), &comatproto.RepoPutRecord_Output{})
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "InvalidSwap", conflict.ErrStr, "a stale writer must not replace an updated record")

	actual, err := comatproto.RepoGetRecord(ctx, account.XRPC, "", input.Collection, account.DID, input.Rkey)
	require.NoError(t, err)
	require.Equal(t, updated.Cid, *actual.Cid)
}
