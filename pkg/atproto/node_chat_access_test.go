package atproto

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/bluesky-social/indigo/util"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/appbsky"
	"stream.place/streamplace/pkg/comatproto"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/placestream"
	"stream.place/streamplace/pkg/spid"
)

func nodeChatPolicy(t *testing.T, denied string) access.Checker {
	t.Helper()
	var entries []access.Entry
	if denied != "" {
		entries = append(entries, access.Entry{Subject: denied, Capability: access.Chat, Effect: access.Deny, Source: access.Stored})
	}
	policy, err := access.NewSnapshot(access.Bootstrap{Authority: "did:web:node.test", Modes: map[access.Capability]access.Mode{access.Chat: access.Open}}, 1, entries)
	require.NoError(t, err)
	return policy
}

func TestNodeChatRestrictionIndexesWithoutSyncOrLiveReplay(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("channel-blocked=%v", blocked), func(t *testing.T) {
			ctx := context.Background()
			atsync, mod, b := offlineSynchronizer(t)
			const author = "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa"
			const streamer = "did:plc:bbbbbbbbbbbbbbbbbbbbbbbb"
			// Neither repo exists: a denied chat record must not trigger a PDS lookup.
			atsync.Access = nodeChatPolicy(t, author)
			if blocked {
				require.NoError(t, mod.CreateBlock(ctx, &model.Block{RKey: "channel-block", RepoDID: streamer, SubjectDID: author}))
			}
			rec := &placestream.ChatMessage{LexiconTypeID: "place.stream.chat.message", Text: "withheld", CreatedAt: time.Now().UTC().Format(util.ISO8601), Streamer: streamer}
			var buf bytes.Buffer
			require.NoError(t, rec.MarshalCBOR(&buf))
			raw := buf.Bytes()
			cid, err := spid.GetCID(rec)
			require.NoError(t, err)
			ch := b.Subscribe(streamer)
			defer b.Unsubscribe(streamer, ch)
			index := func() error {
				return atsync.handleCreateUpdate(ctx, author, syntax.RecordKey("3lmsg000000000"), &raw, cid.String(), syntax.NSID("place.stream.chat.message"), false, false, "")
			}
			require.NoError(t, index())
			if blocked {
				require.NoError(t, mod.DeleteBlock(ctx, "channel-block"))
			}
			messages, err := mod.MostRecentChatMessages(streamer, time.Time{})
			require.NoError(t, err)
			require.Len(t, messages, 1, "retain original record so redelivery cannot become a new live message")
			require.Nil(t, atsync.FilterChatMessage(ctx, streamer, &messages[0]))
			repo, err := mod.GetRepo(author)
			require.NoError(t, err)
			require.Nil(t, repo, "node restriction must not synchronize the author's identity")
			// After unban an indexed record is available as history, never replayed live.
			require.NoError(t, mod.UpdateRepo(&model.Repo{DID: author, Handle: "author.test", Version: "3lrev00000000", PDS: "http://127.0.0.1:1"}))
			require.NoError(t, mod.UpdateRepo(&model.Repo{DID: streamer, Handle: "streamer.test", Version: "3lrev00000000", PDS: "http://127.0.0.1:1"}))
			atsync.Access = nodeChatPolicy(t, "")
			require.NotNil(t, atsync.FilterChatMessage(ctx, streamer, &messages[0]))
			require.NoError(t, index())
			select {
			case <-ch:
				t.Fatal("a denied message was delivered live or replayed after unban")
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

func TestNodeChatRestrictionRemovesReplyPreviewWithoutMutatingRecord(t *testing.T) {
	ctx := context.Background()
	atsync, _, _ := offlineSynchronizer(t)
	atsync.Access = nodeChatPolicy(t, "did:plc:banned")
	parent := &placestream.ChatDefs_MessageView{Author: appbsky.ActorDefs_ProfileViewBasic{Did: "did:plc:banned"}}
	reply := &placestream.ChatDefs_MessageView{Author: appbsky.ActorDefs_ProfileViewBasic{Did: "did:plc:allowed"}, ReplyTo: &placestream.ChatDefs_MessageView_ReplyTo{ChatDefs_MessageView: parent}}
	filtered := atsync.FilterChatMessage(ctx, "did:plc:streamer", reply)
	require.NotNil(t, filtered)
	require.Nil(t, filtered.ReplyTo)
	require.NotNil(t, reply.ReplyTo, "delivery filtering must not rewrite the stored record or shared bus value")
	require.False(t, atsync.ChatAllowed(ctx, "did:plc:streamer", "did:plc:banned"))
	atsync.Access = nil
	require.True(t, atsync.ChatAllowed(ctx, "did:plc:streamer", "did:plc:banned"), "standalone synchronizers preserve legacy channel policy")
}

func TestNodeChatRestrictionWithholdsBlueskyRepliesWithoutSync(t *testing.T) {
	ctx := context.Background()
	atsync, mod, b := offlineSynchronizer(t)
	const author = "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa"
	const streamer = "did:plc:bbbbbbbbbbbbbbbbbbbbbbbb"
	const root = "at://" + streamer + "/app.bsky.feed.post/3lroot0000000"
	atsync.Access = nodeChatPolicy(t, author)
	require.NoError(t, mod.UpdateRepo(&model.Repo{DID: streamer, Handle: "streamer.test", Version: "3lrev00000000", PDS: "http://127.0.0.1:1"}))
	now := time.Now().UTC()
	live := &placestream.Livestream{CreatedAt: now.Format(time.RFC3339)}
	var buf bytes.Buffer
	require.NoError(t, live.MarshalCBOR(&buf))
	rawLive := buf.Bytes()
	require.NoError(t, mod.CreateLivestream(ctx, &model.Livestream{URI: "at://" + streamer + "/place.stream.livestream/3llive0000000", RepoDID: streamer, PostURI: root, CreatedAt: now, Livestream: &rawLive}))
	rootCID, err := spid.GetCID(live)
	require.NoError(t, err)
	ref := comatproto.RepoStrongRef{Uri: root, Cid: rootCID.String()}
	post := &appbsky.FeedPost{Text: "withheld reply", CreatedAt: now.Format(time.RFC3339), Reply: &appbsky.FeedPost_ReplyRef{Parent: ref, Root: ref}}
	buf.Reset()
	require.NoError(t, post.MarshalCBOR(&buf))
	raw := buf.Bytes()
	cid, err := spid.GetCID(post)
	require.NoError(t, err)
	index := func() error {
		return atsync.handleCreateUpdate(ctx, author, syntax.RecordKey("3lreply000000"), &raw, cid.String(), syntax.NSID("app.bsky.feed.post"), false, false, "")
	}
	ch := b.Subscribe(streamer)
	defer b.Unsubscribe(streamer, ch)
	require.NoError(t, index())
	posts, err := mod.ListFeedPosts()
	require.NoError(t, err)
	require.Len(t, posts, 1)
	repo, err := mod.GetRepo(author)
	require.NoError(t, err)
	require.Nil(t, repo)
	require.NoError(t, mod.UpdateRepo(&model.Repo{DID: author, Handle: "author.test", Version: "3lrev00000000", PDS: "http://127.0.0.1:1"}))
	atsync.Access = nodeChatPolicy(t, "")
	require.NoError(t, index())
	select {
	case <-ch:
		t.Fatal("restricted Bluesky reply was delivered live or replayed after unban")
	case <-time.After(100 * time.Millisecond):
	}
}
