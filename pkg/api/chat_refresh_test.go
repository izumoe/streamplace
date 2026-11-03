package api

import (
	"bytes"
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	slogGorm "github.com/orandin/slog-gorm"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/accessctl"
	"stream.place/streamplace/pkg/appbsky"
	"stream.place/streamplace/pkg/atproto"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/log"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/placestream"
	"stream.place/streamplace/pkg/statedb"
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
	stored, err := mod.GetChatMessage(ctx, parentURI)
	require.NoError(t, err)
	require.NotNil(t, stored, "hiding delivery must not remove source records")
	queued, err := stored.ToStreamplaceMessageView()
	require.NoError(t, err)
	require.Nil(t, api.filterChatDelivery(queued))
	require.Nil(t, api.filterChatDelivery(*queued))
	pinView := placestream.ChatDefs_PinnedRecordView{Message: queued}
	require.Nil(t, api.filterChatDelivery(pinView))
	require.Nil(t, api.filterChatDelivery(&pinView))
	postView := appbsky.FeedDefs_PostView{Author: appbsky.ActorDefs_ProfileViewBasic{Did: banned}}
	require.Nil(t, api.filterChatDelivery(postView))
	require.Nil(t, api.filterChatDelivery(&postView))
	sync.Access = nil
	snapshot, err = api.chatRefresh(ctx, streamer, 3)
	require.NoError(t, err)
	require.Len(t, snapshot.Messages, 2)
	require.NotNil(t, snapshot.PinnedComment)
	sync.Access = policy
	queuedSnapshot := api.filterChatDelivery(snapshot).(*chatRefresh)
	require.Len(t, queuedSnapshot.Messages, 1, "a snapshot prepared before restriction must be rechecked at delivery")
	require.Nil(t, queuedSnapshot.PinnedComment)
	require.Len(t, snapshot.Messages, 2, "queued delivery must not mutate another consumer's snapshot")
	require.NotNil(t, snapshot.PinnedComment)
}

func TestQueuedChatDeliveryUsesOnlyNodePolicy(t *testing.T) {
	ctx := t.Context()
	mod, err := model.MakeDB(":memory:")
	require.NoError(t, err)
	const streamer = "did:plc:queued-delivery"
	const author = "did:plc:allowed"
	const banned = "did:plc:banned"
	require.NoError(t, mod.CreateChatAccessRule(ctx, &model.ChatAccessRule{URI: "at://" + streamer + "/place.stream.chat.access/rule", RepoDID: streamer, Action: model.ChatAccessDeny, SubjectType: model.ChatAccessSubjectVerifier, SubjectDID: "did:plc:verifier", CreatedAt: time.Now()}))
	policy, err := access.NewSnapshot(access.Bootstrap{}, 1, []access.Entry{{Subject: banned, Capability: access.Chat, Effect: access.Deny}})
	require.NoError(t, err)
	sync := &atproto.ATProtoSynchronizer{Model: mod, Access: policy}
	require.True(t, sync.ChatAllowed(ctx, streamer, author), "shared preparation applies channel policy")
	queries := 0
	db := mod.(*model.DBModel).DB
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("count_delivery_queries", func(*gorm.DB) { queries++ }))
	api := &StreamplaceAPI{ATSync: sync}
	parent := &placestream.ChatDefs_MessageView{Author: appbsky.ActorDefs_ProfileViewBasic{Did: banned}}
	message := &placestream.ChatDefs_MessageView{Author: appbsky.ActorDefs_ProfileViewBasic{Did: author}, ReplyTo: &placestream.ChatDefs_MessageView_ReplyTo{ChatDefs_MessageView: parent}}
	for range 10 {
		delivered := api.filterChatDelivery(message).(*placestream.ChatDefs_MessageView)
		require.Nil(t, delivered.ReplyTo)
		require.NotNil(t, message.ReplyTo, "delivery must not mutate the shared message")
		require.Nil(t, api.filterChatDelivery(&placestream.ChatDefs_PinnedRecordView{Message: parent}))
	}
	require.Zero(t, queries, "fanout must recheck node policy without repeating database-backed channel authorization")
}

func TestChatRefreshSharedByActiveViewers(t *testing.T) {
	ctx := t.Context()
	mod, err := model.MakeDB(":memory:")
	require.NoError(t, err)
	const streamer = "did:plc:refresh-fanout"
	const author = "did:plc:author"
	require.NoError(t, mod.UpdateRepo(&model.Repo{DID: author, Handle: "author.test"}))
	now := time.Now().UTC()
	var buf bytes.Buffer
	require.NoError(t, (&placestream.ChatMessage{Text: "hello", Streamer: streamer, CreatedAt: now.Format(time.RFC3339Nano)}).MarshalCBOR(&buf))
	raw := buf.Bytes()
	require.NoError(t, mod.CreateChatMessage(ctx, &model.ChatMessage{URI: "at://" + author + "/place.stream.chat.message/one", CID: "cid-one", RepoDID: author, StreamerRepoDID: streamer, CreatedAt: now, IndexedAt: &now, ChatMessage: &raw}))
	state, err := statedb.MakeDB(ctx, &config.CLI{DBURL: ":memory:"}, nil, nil)
	require.NoError(t, err)
	b := bus.NewBus()
	ctl, err := accessctl.New(ctx, access.Bootstrap{Authority: "did:web:node.test"}, state, b)
	require.NoError(t, err)
	api := &StreamplaceAPI{Model: mod, ATSync: &atproto.ATProtoSynchronizer{Model: mod, Access: ctl}, CLI: &config.CLI{BroadcasterHost: "node.test"}, Bus: b}
	var historyQueries, decorations atomic.Int64
	require.NoError(t, mod.(*model.DBModel).DB.Callback().Query().After("gorm:query").Register("count_refresh_queries", func(db *gorm.DB) {
		switch db.Statement.Table {
		case "chat_messages":
			historyQueries.Add(1)
		case "moderation_delegations":
			decorations.Add(1)
		}
	}))
	first, second := b.Subscribe(streamer), b.Subscribe(streamer)
	defer b.Unsubscribe(streamer, first)
	defer b.Unsubscribe(streamer, second)
	firstCtx, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()
	releaseFirst := api.subscribeChatRefresh(firstCtx, streamer)
	releaseSecond := api.subscribeChatRefresh(ctx, streamer)
	defer func() {
		if releaseFirst != nil {
			releaseFirst()
		}
		if releaseSecond != nil {
			releaseSecond()
		}
	}()
	// A policy event is prepared once even when several sockets need its result.
	b.Publish(access.Topic, access.Change{Revision: 7, ChatChanged: true})
	receive := func(ch <-chan bus.Message, count int, revision uint64) {
		t.Helper()
		select {
		case event := <-ch:
			pending, ok := event.(*pendingChatRefresh)
			require.True(t, ok)
			select {
			case <-pending.ready:
			case <-time.After(time.Second):
				t.Fatal("chat refresh did not finish")
			}
			require.NoError(t, pending.err)
			snapshot := pending.snapshot
			require.Len(t, snapshot.Messages, count)
			require.Equal(t, revision, snapshot.Revision)
		case <-time.After(time.Second):
			t.Fatal("missing shared chat refresh")
		}
	}
	receive(first, 1, 7)
	receive(second, 1, 7)
	require.Equal(t, int64(1), historyQueries.Load())
	require.Equal(t, int64(1), decorations.Load())
	releaseFirst()
	releaseFirst = nil
	cancelFirst()
	// The first socket leaving must not cancel the remaining socket's worker.
	expiry := time.Now().Add(100 * time.Millisecond)
	_, err = ctl.Create(ctx, author, access.Chat, access.Deny, "temporary", &expiry)
	require.NoError(t, err)
	receive(second, 0, 1)
	require.Equal(t, int64(2), historyQueries.Load())
	require.Eventually(t, func() bool { return ctl.Check(author, access.Chat).Allowed }, time.Second, time.Millisecond)
	require.NoError(t, ctl.Refresh(ctx))
	receive(second, 1, 1) // Expiry changes policy without advancing its revision.
	require.Equal(t, int64(3), historyQueries.Load())
	require.Equal(t, int64(2), decorations.Load())
	releaseSecond()
	releaseSecond = nil
	require.Empty(t, api.chatRefreshes, "last socket release must reap the worker and its registry entry")
}

func TestChatRefreshFailureRequestsReconnect(t *testing.T) {
	mod, err := model.MakeDB(":memory:")
	require.NoError(t, err)
	b := bus.NewBus()
	api := &StreamplaceAPI{Model: mod, ATSync: &atproto.ATProtoSynchronizer{Model: mod}, CLI: &config.CLI{}, Bus: b}
	const streamer = "did:plc:refresh-failure"
	messages := b.Subscribe(streamer)
	defer b.Unsubscribe(streamer, messages)
	ctx := log.WithDebugValue(log.WithLogValues(t.Context(), "func", "chat-refresh-test"), map[string]map[string]int{"func": {"chat-refresh-test": 1}})
	release := api.subscribeChatRefresh(ctx, streamer)
	defer release()
	db, err := mod.(*model.DBModel).DB.DB()
	require.NoError(t, err)
	require.NoError(t, db.Close())
	var logs bytes.Buffer
	previous := slog.Default()
	handler := slog.NewJSONHandler(&logs, nil)
	slog.SetDefault(slog.New(handler))
	mod.(*model.DBModel).DB.Logger = slogGorm.New(slogGorm.WithHandler(handler))
	defer slog.SetDefault(previous)
	b.Publish(access.Topic, access.Change{Revision: 1, ChatChanged: true})
	select {
	case event := <-messages:
		pending, ok := event.(*pendingChatRefresh)
		require.True(t, ok)
		select {
		case <-pending.ready:
		case <-time.After(time.Second):
			t.Fatal("chat refresh did not finish")
		}
		require.ErrorContains(t, pending.err, "database is closed", "failed hydration must make sockets reconnect and clear cached chat")
	case <-time.After(time.Second):
		t.Fatal("missing refresh failure")
	}
	require.Contains(t, logs.String(), "could not refresh chat policy")
	require.Contains(t, logs.String(), "database is closed")
}

func TestLastChatViewerCancelsPendingHistoryQuery(t *testing.T) {
	mod, err := model.MakeDB(":memory:")
	require.NoError(t, err)
	db := mod.(*model.DBModel).DB
	var logs bytes.Buffer
	db.Logger = slogGorm.New(slogGorm.WithHandler(slog.NewJSONHandler(&logs, nil)))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	// Hold the sole SQLite connection so history must wait for a pool slot.
	held, err := sqlDB.Conn(t.Context())
	require.NoError(t, err)
	defer held.Close()
	entered := make(chan struct{})
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("history_query_entered", func(tx *gorm.DB) {
		if tx.Statement.Table == "chat_messages" {
			close(entered)
		}
	}))
	b := bus.NewBus()
	api := &StreamplaceAPI{Model: mod, ATSync: &atproto.ATProtoSynchronizer{Model: mod}, CLI: &config.CLI{}, Bus: b}
	release := api.subscribeChatRefresh(t.Context(), "did:plc:cancel-refresh")
	b.Publish(access.Topic, access.Change{Revision: 1, ChatChanged: true})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("history query did not start")
	}
	stopped := make(chan struct{})
	go func() { release(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		require.NoError(t, held.Close())
		<-stopped
		t.Fatal("last viewer release did not cancel the pending history query")
	}
	require.Empty(t, api.chatRefreshes)
	require.Contains(t, logs.String(), "context canceled")
}
