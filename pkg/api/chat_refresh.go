package api

import (
	"context"
	"fmt"
	"time"

	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/appbsky"
	"stream.place/streamplace/pkg/atproto"
	"stream.place/streamplace/pkg/log"
	"stream.place/streamplace/pkg/placestream"
)

// chatRefresh is a node-local delivery snapshot, not an AT Protocol record.
type chatRefresh struct {
	Type          string                                 `json:"$type"`
	Revision      uint64                                 `json:"revision"`
	Messages      []placestream.ChatDefs_MessageView     `json:"messages"`
	PinnedComment *placestream.ChatDefs_PinnedRecordView `json:"pinnedComment"`
}

func (a *StreamplaceAPI) chatRefresh(ctx context.Context, streamer string, revision uint64) (*chatRefresh, error) {
	snapshot := &chatRefresh{Type: "place.stream.chat.refresh", Revision: revision, Messages: []placestream.ChatDefs_MessageView{}}
	var since time.Time
	if a.CLI.ChatMessageRetention > 0 {
		since = time.Now().Add(-a.CLI.ChatMessageRetention)
	}
	messages, err := a.Model.MostRecentChatMessages(ctx, streamer, since)
	if err != nil {
		return nil, fmt.Errorf("loading chat history: %w", err)
	}
	issuer := fmt.Sprintf("did:web:%s", a.CLI.BroadcasterHost)
	for _, message := range messages {
		filtered := a.ATSync.FilterChatMessage(ctx, streamer, &message)
		if filtered == nil {
			continue
		}
		if err := atproto.AddModBadgeIfApplicable(ctx, filtered, streamer, issuer, a.Model); err != nil {
			return nil, fmt.Errorf("decorating chat history: %w", err)
		}
		a.ATSync.DecorateVerification(ctx, streamer, filtered)
		if filtered.Author.Handle == "" || filtered.Author.Handle == "handle.invalid" {
			filtered.Author.Handle = a.ATSync.ResolveAuthorHandle(ctx, filtered.Author.Did)
		}
		snapshot.Messages = append(snapshot.Messages, *filtered)
	}
	pin, err := a.Model.GetActivePinnedRecord(ctx, streamer)
	if err != nil {
		return nil, fmt.Errorf("loading chat pin: %w", err)
	}
	if pin == nil {
		return snapshot, nil
	}
	view, err := pin.ToStreamplacePinnedRecordView()
	if err != nil {
		return nil, err
	}
	message, err := a.Model.GetChatMessage(ctx, view.Record.PinnedMessage)
	if err != nil {
		return nil, err
	}
	if message == nil || message.DeletedAt != nil {
		return snapshot, nil
	}
	original, err := message.ToStreamplaceMessageView()
	if err != nil {
		return nil, err
	}
	view.Message = a.ATSync.FilterChatMessage(ctx, streamer, original)
	if view.Message == nil {
		return snapshot, nil
	}
	if view.Message.Author.Handle == "" || view.Message.Author.Handle == "handle.invalid" {
		view.Message.Author.Handle = a.ATSync.ResolveAuthorHandle(ctx, view.Message.Author.Did)
	}
	if view.Record.PinnedBy == nil {
		view.Record.PinnedBy = &streamer
	}
	profile, err := a.Model.GetChatProfile(ctx, *view.Record.PinnedBy)
	if err != nil {
		return nil, err
	}
	if profile != nil {
		profileView, err := profile.ToStreamplaceChatProfile()
		if err != nil {
			return nil, err
		}
		view.PinnedBy = &profileView
	}
	snapshot.PinnedComment = &view
	return snapshot, nil
}

// Recheck queued deliveries against current policy, including initial hydration.
func (a *StreamplaceAPI) filterChatDelivery(message any) any {
	switch msg := message.(type) {
	case *chatRefresh:
		if msg == nil {
			return nil
		}
		filtered := *msg
		filtered.Messages = make([]placestream.ChatDefs_MessageView, 0, len(msg.Messages))
		for _, entry := range msg.Messages {
			if view := a.filterNodeChatMessage(&entry); view != nil {
				filtered.Messages = append(filtered.Messages, *view)
			}
		}
		filtered.PinnedComment = nil
		if pin := a.filterChatDelivery(msg.PinnedComment); pin != nil {
			filtered.PinnedComment = pin.(*placestream.ChatDefs_PinnedRecordView)
		}
		return &filtered
	case appbsky.FeedDefs_PostView:
		if !a.ATSync.NodeChatAllowed(msg.Author.Did) {
			return nil
		}
		return msg
	case *appbsky.FeedDefs_PostView:
		if msg == nil || !a.ATSync.NodeChatAllowed(msg.Author.Did) {
			return nil
		}
		return msg
	case placestream.ChatDefs_MessageView:
		filtered := a.filterNodeChatMessage(&msg)
		if filtered == nil {
			return nil
		}
		return filtered
	case *placestream.ChatDefs_MessageView:
		filtered := a.filterNodeChatMessage(msg)
		if filtered == nil {
			return nil
		}
		return filtered
	case placestream.ChatDefs_PinnedRecordView:
		return a.filterChatDelivery(&msg)
	case *placestream.ChatDefs_PinnedRecordView:
		if msg == nil {
			return nil
		}
		filtered := *msg
		if msg.Message != nil {
			filtered.Message = a.filterNodeChatMessage(msg.Message)
			if filtered.Message == nil {
				return nil
			}
		}
		return &filtered
	}
	return message
}

// Shared preparation applies channel rules. Fanout only rechecks the current
// in-memory node policy so the database work does not grow with viewer count.
func (a *StreamplaceAPI) filterNodeChatMessage(message *placestream.ChatDefs_MessageView) *placestream.ChatDefs_MessageView {
	if message == nil || !a.ATSync.NodeChatAllowed(message.Author.Did) {
		return nil
	}
	if message.ReplyTo != nil && message.ReplyTo.ChatDefs_MessageView != nil {
		reply := a.filterNodeChatMessage(message.ReplyTo.ChatDefs_MessageView)
		if reply != message.ReplyTo.ChatDefs_MessageView {
			filtered := *message
			filtered.ReplyTo = nil
			if reply != nil {
				filtered.ReplyTo = &placestream.ChatDefs_MessageView_ReplyTo{ChatDefs_MessageView: reply}
			}
			return &filtered
		}
	}
	return message
}

// A worker exists only while viewers are subscribed to its streamer.
type chatRefreshWorker struct {
	viewers  int
	cancel   context.CancelFunc
	done     chan struct{}
	overflow chan struct{}
}

type chatRefreshFailed struct{}

func (a *StreamplaceAPI) subscribeChatRefresh(ctx context.Context, streamer string) func() {
	a.chatRefreshMu.Lock()
	if a.chatRefreshes == nil {
		a.chatRefreshes = map[string]*chatRefreshWorker{}
	}
	worker := a.chatRefreshes[streamer]
	if worker != nil {
		select {
		case <-worker.overflow:
			worker = nil
		default:
		}
	}
	if worker == nil {
		workerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		worker = &chatRefreshWorker{cancel: cancel, done: make(chan struct{}), overflow: make(chan struct{})}
		changes := a.Bus.SubscribeWithBacklogLimit(access.Topic, func() {
			close(worker.overflow)
			cancel()
		})
		a.chatRefreshes[streamer] = worker
		go func() {
			defer close(worker.done)
			defer a.Bus.Unsubscribe(access.Topic, changes)
			defer func() {
				select {
				case <-worker.overflow:
					a.Bus.Publish(streamer, chatRefreshFailed{})
				default:
				}
			}()
			for {
				select {
				case <-workerCtx.Done():
					return
				case event := <-changes:
					change, ok := event.(access.Change)
					if !ok || !change.ChatChanged {
						continue
					}
					snapshot, err := a.chatRefresh(workerCtx, streamer, change.Revision)
					if workerCtx.Err() != nil {
						return
					}
					if err != nil {
						log.Error(workerCtx, "could not refresh chat policy", "error", err)
						a.Bus.Publish(streamer, chatRefreshFailed{})
						continue
					}
					a.chatRefreshMu.Lock()
					if workerCtx.Err() == nil && a.chatRefreshes[streamer] == worker {
						a.Bus.Publish(streamer, snapshot)
					}
					a.chatRefreshMu.Unlock()
				}
			}
		}()
	}
	worker.viewers++
	a.chatRefreshMu.Unlock()
	return func() {
		a.chatRefreshMu.Lock()
		worker.viewers--
		last := worker.viewers == 0
		if last {
			worker.cancel()
			if a.chatRefreshes[streamer] == worker {
				delete(a.chatRefreshes, streamer)
			}
		}
		a.chatRefreshMu.Unlock()
		if last {
			<-worker.done
		}
	}
}
