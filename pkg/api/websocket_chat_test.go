package api

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/julienschmidt/httprouter"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/appbsky"
	"stream.place/streamplace/pkg/atproto"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/localdb"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/placestream"
)

func TestWebsocketChatSnapshotPrecedesConcurrentEvents(t *testing.T) {
	for _, initial := range []bool{true, false} {
		for _, event := range []string{"message", "block", "disconnect"} {
			name := "policy/" + event
			if initial {
				name = "initial/" + event
			}
			t.Run(name, func(t *testing.T) {
				ctx := t.Context()
				const streamer = "did:key:zChatSnapshot"
				const author = "did:plc:chat-author"
				mod, err := model.MakeDB(":memory:")
				require.NoError(t, err)
				db := mod.(*model.DBModel).DB
				sqlDB, err := db.DB()
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
				require.NoError(t, mod.UpdateRepo(&model.Repo{DID: author, Handle: "author.test"}))
				require.NoError(t, mod.UpdateRepo(&model.Repo{DID: streamer, Handle: "streamer.test"}))
				now := time.Now().UTC()
				var liveRecord bytes.Buffer
				require.NoError(t, (&placestream.Livestream{CreatedAt: now.Format(time.RFC3339Nano)}).MarshalCBOR(&liveRecord))
				liveBytes := liveRecord.Bytes()
				require.NoError(t, mod.CreateLivestream(ctx, &model.Livestream{URI: "at://" + streamer + "/place.stream.livestream/live", CID: "live-cid", RepoDID: streamer, CreatedAt: now, Livestream: &liveBytes}))
				// Cancellation errors are asserted at the query boundary below.
				if event == "disconnect" {
					db.Logger = logger.Discard
				}
				addMessage := func(key string) *placestream.ChatDefs_MessageView {
					t.Helper()
					now := time.Now().UTC()
					var encoded bytes.Buffer
					require.NoError(t, (&placestream.ChatMessage{Text: key, Streamer: streamer, CreatedAt: now.Format(time.RFC3339Nano)}).MarshalCBOR(&encoded))
					raw := encoded.Bytes()
					uri := "at://" + author + "/place.stream.chat.message/" + key
					require.NoError(t, mod.CreateChatMessage(ctx, &model.ChatMessage{URI: uri, CID: "cid-" + key, RepoDID: author, StreamerRepoDID: streamer, CreatedAt: now, IndexedAt: &now, ChatMessage: &raw}))
					stored, err := mod.GetChatMessage(ctx, uri)
					require.NoError(t, err)
					view, err := stored.ToStreamplaceMessageView()
					require.NoError(t, err)
					return view
				}
				old := addMessage("old")
				local, err := localdb.MakeDB(":memory:")
				require.NoError(t, err)
				require.NoError(t, local.CreateSegment(&localdb.Segment{
					ID: "segment", RepoDID: streamer, StartTime: now, Published: true,
					MediaData: &localdb.SegmentMediaData{
						Video: []*localdb.SegmentMediadataVideo{{Codec: "h264", Width: 1280, Height: 720, FPSNum: 30, FPSDen: 1}},
						Audio: []*localdb.SegmentMediadataAudio{{Codec: "opus", Rate: 48000, Channels: 2}},
					},
				}))
				localSQL, err := local.(*localdb.LocalDatabase).DB.DB()
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, localSQL.Close()) })
				b := bus.NewBus()
				api := &StreamplaceAPI{CLI: &config.CLI{BroadcasterHost: "node.test"}, Model: mod, LocalDB: local, Bus: b, MediaManager: &media.MediaManager{}, ATSync: &atproto.ATProtoSynchronizer{Model: mod}}
				var armed atomic.Bool
				armed.Store(initial)
				loaded, resume := make(chan struct{}), make(chan struct{})
				queryReturned := make(chan error, 1)
				var resumeOnce sync.Once
				unblock := func() { resumeOnce.Do(func() { close(resume) }) }
				t.Cleanup(unblock)
				require.NoError(t, db.Callback().Query().After("gorm:after_query").Register("pause_chat_snapshot", func(tx *gorm.DB) {
					if tx.Statement.Table == "chat_messages" && armed.CompareAndSwap(true, false) {
						close(loaded)
						select {
						case <-resume:
						case <-tx.Statement.Context.Done():
						}
						queryReturned <- tx.Statement.Context.Err()
					}
				}))
				router := httprouter.New()
				router.GET("/websocket/:repoDID", api.HandleWebsocket(ctx))
				server := httptest.NewServer(router)
				t.Cleanup(server.Close)
				conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/websocket/"+streamer, nil)
				require.NoError(t, err)
				t.Cleanup(func() { conn.Close() })
				frames := make(chan map[string]any, 20)
				readDone := make(chan struct{})
				go func() {
					defer close(readDone)
					for {
						var frame map[string]any
						if err := conn.ReadJSON(&frame); err != nil {
							return
						}
						typ, _ := frame["$type"].(string)
						if typ == "place.stream.chat.defs#messageView" || typ == "place.stream.chat.refresh" || typ == "place.stream.defs#blockView" {
							select {
							case frames <- frame:
							case <-ctx.Done():
								return
							}
						}
					}
				}()
				receive := func() map[string]any {
					t.Helper()
					select {
					case frame := <-frames:
						return frame
					case <-time.After(3 * time.Second):
						t.Fatal("missing chat frame")
						return nil
					}
				}
				if !initial {
					require.Equal(t, old.Uri, receive()["uri"])
					armed.Store(true)
					b.Publish(access.Topic, access.Change{Revision: 1, ChatChanged: true})
				}
				select {
				case <-loaded:
				case <-time.After(3 * time.Second):
					t.Fatal("snapshot query did not reach barrier")
				}
				if event == "disconnect" {
					require.NoError(t, conn.Close())
					select {
					case err := <-queryReturned:
						require.ErrorIs(t, err, context.Canceled)
					case <-time.After(3 * time.Second):
						t.Fatal("socket close did not cancel its pending snapshot query")
					}
					<-readDone
					require.Eventually(t, func() bool {
						api.chatRefreshMu.Lock()
						defer api.chatRefreshMu.Unlock()
						return len(api.chatRefreshes) == 0
					}, time.Second, time.Millisecond)
					return
				}
				if event == "message" {
					b.Publish(streamer, addMessage("new"))
				} else {
					var encoded bytes.Buffer
					record := appbsky.GraphBlock{Subject: author, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
					require.NoError(t, record.MarshalCBOR(&encoded))
					block := &model.Block{RKey: "block-author", RepoDID: streamer, SubjectDID: author, Record: encoded.Bytes(), CreatedAt: time.Now().UTC()}
					require.NoError(t, mod.CreateBlock(ctx, block))
					b.Publish(streamer, placestream.Defs_BlockView{LexiconTypeID: "place.stream.defs#blockView", Blocker: appbsky.ActorDefs_ProfileViewBasic{Did: streamer}, Record: record})
				}
				// A live/moderation event must wait behind the already captured history.
				select {
				case frame := <-frames:
					t.Fatalf("event overtook in-flight snapshot: %s", frame["$type"])
				case <-time.After(100 * time.Millisecond):
				}
				unblock()
				first := receive()
				if initial {
					require.Equal(t, old.Uri, first["uri"])
				} else {
					require.Equal(t, "place.stream.chat.refresh", first["$type"])
					messages := first["messages"].([]any)
					require.Len(t, messages, 1)
					require.Equal(t, old.Uri, messages[0].(map[string]any)["uri"])
				}
				next := receive()
				if event == "message" {
					require.True(t, strings.HasSuffix(next["uri"].(string), "/new"))
				} else {
					require.Equal(t, "place.stream.defs#blockView", next["$type"])
				}
				require.NoError(t, conn.Close())
				<-readDone
			})
		}
	}
}
