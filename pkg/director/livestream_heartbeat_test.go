package director

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	slogGorm "github.com/orandin/slog-gorm"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/placestream"
)

func TestLivestreamHeartbeatPreservesConcurrentEdits(t *testing.T) {
	var logs bytes.Buffer
	logger, gormLogger := slog.Default(), config.GormLogger
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	config.GormLogger = slogGorm.New(slogGorm.WithHandler(slog.Default().Handler()))
	t.Cleanup(func() { slog.SetDefault(logger); config.GormLogger = gormLogger })
	const did = "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa"
	const currentCID = "bafyreigrquyde7kfqjfwdtbgmcf6ugzk6kxeb3k3z55o47hf35ihur6xrq"
	mod, err := model.MakeDB(":memory:")
	require.NoError(t, err)
	db, err := mod.(*model.DBModel).DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, mod.UpdateRepo(&model.Repo{DID: did, Handle: "publisher.test"}))
	var indexed bytes.Buffer
	require.NoError(t, (&placestream.Livestream{Title: "Indexed old title", CreatedAt: "2026-11-12T00:00:00Z"}).MarshalCBOR(&indexed))
	data := indexed.Bytes()
	require.NoError(t, mod.CreateLivestream(t.Context(), &model.Livestream{URI: "at://" + did + "/place.stream.livestream/test", CID: "old-indexed-cid", RepoDID: did, CreatedAt: time.Now(), Livestream: &data}))
	for _, tc := range []struct {
		name                         string
		missing, wrongType, conflict bool
	}{
		{name: "newer PDS edits"},
		{name: "missing CID", missing: true},
		{name: "wrong record type", wrongType: true},
		{name: "edit after snapshot", conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh := map[string]json.RawMessage{}
			require.NoError(t, json.Unmarshal([]byte(`{"$type":"place.stream.livestream","title":"Edited on another client","createdAt":"2026-11-12T00:00:00Z","lastSeenAt":"2026-11-12T00:01:00Z","endedAt":"2026-11-12T00:02:00Z","futureExtension":{"count":9007199254740993,"enabled":true}}`), &fresh))
			if tc.wrongType {
				fresh["$type"] = json.RawMessage(`"place.stream.chat.message"`)
			}
			written := make(chan map[string]json.RawMessage, 1)
			pds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/xrpc/com.atproto.server.createSession":
					require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"did": did, "handle": "publisher.test", "accessJwt": "test-access", "refreshJwt": "test-refresh"}))
				case "/xrpc/com.atproto.repo.getRecord":
					out := map[string]any{"value": fresh}
					if !tc.missing {
						out["cid"] = currentCID
					}
					require.NoError(t, json.NewEncoder(w).Encode(out))
				case "/xrpc/com.atproto.repo.putRecord":
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					var request struct {
						Repo, Collection, Rkey, SwapRecord string
						Record                             map[string]json.RawMessage
					}
					require.NoError(t, json.Unmarshal(body, &request))
					require.Equal(t, did, request.Repo)
					require.Equal(t, "place.stream.livestream", request.Collection)
					require.Equal(t, "test", request.Rkey)
					if !tc.missing {
						require.Equal(t, currentCID, request.SwapRecord)
					}
					if tc.conflict && request.SwapRecord != "" {
						w.WriteHeader(http.StatusBadRequest)
						require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"error": "InvalidSwap", "message": "Record changed after read"}))
						return
					}
					written <- request.Record
					require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"cid": currentCID}))
				default:
					http.NotFound(w, r)
				}
			}))
			defer pds.Close()
			require.NoError(t, mod.UpdateRepo(&model.Repo{DID: did, Handle: "publisher.test", PDS: pds.URL}))
			ss := &StreamSession{repoDID: did, mod: mod, cli: &config.CLI{DevAccountCreds: map[string]string{did: "test-password"}}}
			err := ss.doUpdateLivestream(t.Context(), did)
			if tc.missing || tc.wrongType || tc.conflict {
				require.Error(t, err)
				require.Empty(t, written)
				require.True(t, ss.lastLivestreamTime.IsZero())
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, written)
			got := <-written
			var seen string
			require.NoError(t, json.Unmarshal(got["lastSeenAt"], &seen))
			seenAt, err := time.Parse(time.RFC3339, seen)
			require.NoError(t, err)
			require.WithinDuration(t, time.Now(), seenAt, 5*time.Second)
			require.Len(t, got, len(fresh))
			for field, value := range fresh {
				if field != "lastSeenAt" {
					require.Equal(t, value, got[field], "heartbeat must preserve PDS field %s", field)
				}
			}
		})
	}
}
