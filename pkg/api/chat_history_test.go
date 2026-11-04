package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/accessctl"
	"stream.place/streamplace/pkg/appbsky"
	"stream.place/streamplace/pkg/atproto"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/statedb"
)

func TestChatHistoryWithholdsRestrictedRepliesWithoutChangingRecords(t *testing.T) {
	ctx := t.Context()
	const streamer = "did:plc:history-streamer"
	const restricted = "did:plc:restricted-author"
	const allowed = "did:plc:allowed-author"
	mod, err := model.MakeDB(":memory:")
	require.NoError(t, err)
	modelDB, err := mod.(*model.DBModel).DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, modelDB.Close()) })
	state, err := statedb.MakeDB(ctx, &config.CLI{DBURL: ":memory:"}, nil, nil)
	require.NoError(t, err)
	stateDB, err := state.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stateDB.Close()) })
	controller, err := accessctl.New(ctx, access.Bootstrap{Authority: "did:web:node.test"}, state, nil)
	require.NoError(t, err)
	require.NoError(t, mod.UpdateRepo(&model.Repo{DID: streamer, Handle: "streamer.test", Version: "indexed"}))
	now := time.Now().UTC()
	var record bytes.Buffer
	require.NoError(t, (&appbsky.FeedPost{Text: "a chat reply", CreatedAt: now.Format(time.RFC3339Nano)}).MarshalCBOR(&record))
	raw := record.Bytes()
	root := streamer
	for _, did := range []string{restricted, allowed} {
		require.NoError(t, mod.UpdateRepo(&model.Repo{DID: did, Handle: "author.test"}))
		require.NoError(t, mod.CreateFeedPost(ctx, &model.FeedPost{
			URI: "at://" + did + "/app.bsky.feed.post/reply", CID: did + "-cid", RepoDID: did,
			Type: "reply", ReplyRootRepoDID: &root, FeedPost: &raw, CreatedAt: now, IndexedAt: &now,
		}))
	}
	api := &StreamplaceAPI{Model: mod, ATSync: &atproto.ATProtoSynchronizer{Model: mod, Access: controller}}
	router := httprouter.New()
	router.GET("/api/chat/:repoDID", api.HandleChat(ctx))
	history := func() []string {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/chat/"+streamer, nil))
		require.Equal(t, http.StatusOK, response.Code)
		var replies []appbsky.FeedDefs_PostView
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &replies))
		authors := make([]string, 0, len(replies))
		for _, reply := range replies {
			authors = append(authors, reply.Author.Did)
		}
		return authors
	}
	require.ElementsMatch(t, []string{restricted, allowed}, history())
	denial, err := controller.Create(ctx, restricted, access.Chat, access.Deny, "local restriction", nil)
	require.NoError(t, err)
	require.Equal(t, []string{allowed}, history())
	stored, err := mod.GetFeedPost("at://" + restricted + "/app.bsky.feed.post/reply")
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, raw, *stored.FeedPost, "withholding delivery preserves the original AT Protocol record")
	block, err := mod.GetUserBlock(ctx, streamer, restricted)
	require.NoError(t, err)
	require.Nil(t, block, "node access must not create a Bluesky block")
	require.NoError(t, controller.Delete(ctx, denial.ID))
	require.ElementsMatch(t, []string{restricted, allowed}, history(), "unbanning restores indexed replies without reinserting them")
	// A local restriction may index a reply that an existing Bluesky block
	// withheld from delivery. Removing the local restriction must keep that block.
	var blockedRecord bytes.Buffer
	require.NoError(t, (&appbsky.GraphBlock{Subject: restricted, CreatedAt: now.Format(time.RFC3339Nano)}).MarshalCBOR(&blockedRecord))
	require.NoError(t, mod.CreateBlock(ctx, &model.Block{RKey: "existing-block", CID: "block-cid", RepoDID: streamer, SubjectDID: restricted, Record: blockedRecord.Bytes()}))
	denial, err = controller.Create(ctx, restricted, access.Chat, access.Deny, "another local restriction", nil)
	require.NoError(t, err)
	blockedURI := "at://" + restricted + "/app.bsky.feed.post/during-ban"
	require.NoError(t, mod.CreateFeedPost(ctx, &model.FeedPost{
		URI: blockedURI, CID: "during-ban-cid", RepoDID: restricted, Type: "reply",
		ReplyRootRepoDID: &root, FeedPost: &raw, CreatedAt: now, IndexedAt: &now,
	}))
	require.NoError(t, controller.Delete(ctx, denial.ID))
	require.Equal(t, []string{allowed}, history(), "node unban must preserve the streamer's independent Bluesky block")
	block, err = mod.GetUserBlock(ctx, streamer, restricted)
	require.NoError(t, err)
	require.NotNil(t, block)
	require.Equal(t, blockedRecord.Bytes(), block.Record)
	stored, err = mod.GetFeedPost(blockedURI)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, raw, *stored.FeedPost)

}
