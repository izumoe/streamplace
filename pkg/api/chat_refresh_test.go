package api

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/appbsky"
	"stream.place/streamplace/pkg/atproto"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/placestream"
)

func TestChatRefreshFiltersHistoryRepliesAndPins(t *testing.T) {
	ctx := context.Background()
	mod, err := model.MakeDB(":memory:")
	require.NoError(t, err)
	const streamer = "did:plc:streamer"
	const banned = "did:plc:banned"
	const allowed = "did:plc:allowed"
	for _, did := range []string{streamer, banned, allowed} {
		require.NoError(t, mod.UpdateRepo(&model.Repo{DID: did, Handle: "author.test"}))
	}
	now := time.Now().UTC()
	parentURI := "at://" + banned + "/place.stream.chat.message/1"
	parentCID := "banned-cid"
	for _, entry := range []struct {
		did, cid, uri string
		reply         *string
	}{{banned, "banned-cid", parentURI, nil}, {allowed, "allowed-cid", "at://" + allowed + "/place.stream.chat.message/2", &parentCID}} {
		rec := &placestream.ChatMessage{Text: "message", Streamer: streamer, CreatedAt: now.Format(time.RFC3339Nano)}
		var buf bytes.Buffer
		require.NoError(t, rec.MarshalCBOR(&buf))
		raw := buf.Bytes()
		require.NoError(t, mod.CreateChatMessage(ctx, &model.ChatMessage{URI: entry.uri, CID: entry.cid, RepoDID: entry.did, StreamerRepoDID: streamer, CreatedAt: now, IndexedAt: &now, ChatMessage: &raw, ReplyToCID: entry.reply}))
	}
	require.NoError(t, mod.CreatePinnedRecord(ctx, &model.PinnedRecord{Uri: "at://" + streamer + "/place.stream.chat.pinnedRecord/1", RepoDID: streamer, PinnedMessage: parentURI, PinnedBy: streamer, CreatedAt: now, IndexedAt: &now, CID: "pin-cid"}))
	policy, err := access.NewSnapshot(access.Bootstrap{Authority: "did:web:node.test", Modes: map[access.Capability]access.Mode{access.Chat: access.Open}}, 2, []access.Entry{{Subject: banned, Capability: access.Chat, Effect: access.Deny, Source: access.Stored}})
	require.NoError(t, err)
	sync := &atproto.ATProtoSynchronizer{Model: mod, Access: policy}
	api := &StreamplaceAPI{Model: mod, ATSync: sync, CLI: &config.CLI{BroadcasterHost: "node.test"}}
	snapshot, err := api.chatRefresh(ctx, streamer, 2)
	require.NoError(t, err)
	require.Len(t, snapshot.Messages, 1)
	require.Equal(t, allowed, snapshot.Messages[0].Author.Did)
	require.Nil(t, snapshot.Messages[0].ReplyTo)
	require.Nil(t, snapshot.PinnedComment)
	stored, err := mod.GetChatMessage(parentURI)
	require.NoError(t, err)
	require.NotNil(t, stored, "hiding delivery must not remove source records")
	queued, err := stored.ToStreamplaceMessageView()
	require.NoError(t, err)
	require.Nil(t, api.filterChatDelivery(ctx, streamer, queued))
	require.Nil(t, api.filterChatDelivery(ctx, streamer, *queued))
	pinView := placestream.ChatDefs_PinnedRecordView{Message: queued}
	require.Nil(t, api.filterChatDelivery(ctx, streamer, pinView))
	require.Nil(t, api.filterChatDelivery(ctx, streamer, &pinView))
	postView := appbsky.FeedDefs_PostView{Author: appbsky.ActorDefs_ProfileViewBasic{Did: banned}}
	require.Nil(t, api.filterChatDelivery(ctx, streamer, postView))
	require.Nil(t, api.filterChatDelivery(ctx, streamer, &postView))
	sync.Access = nil
	snapshot, err = api.chatRefresh(ctx, streamer, 3)
	require.NoError(t, err)
	require.Len(t, snapshot.Messages, 2)
	require.NotNil(t, snapshot.PinnedComment)
	sync.Access = policy
	queuedSnapshot := api.filterChatDelivery(ctx, streamer, snapshot).(*chatRefresh)
	require.Len(t, queuedSnapshot.Messages, 1, "a snapshot prepared before restriction must be rechecked at delivery")
	require.Nil(t, queuedSnapshot.PinnedComment)
	require.Len(t, snapshot.Messages, 2, "queued delivery must not mutate another consumer's snapshot")
	require.NotNil(t, snapshot.PinnedComment)
}
