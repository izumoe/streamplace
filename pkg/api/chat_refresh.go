package api

import (
	"context"
	"fmt"
	"time"

	"stream.place/streamplace/pkg/appbsky"
	"stream.place/streamplace/pkg/atproto"
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
	messages, err := a.Model.MostRecentChatMessages(streamer, since)
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
	message, err := a.Model.GetChatMessage(view.Record.PinnedMessage)
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
func (a *StreamplaceAPI) filterChatDelivery(ctx context.Context, streamer string, message any) any {
	switch msg := message.(type) {
	case *chatRefresh:
		if msg == nil {
			return nil
		}
		filtered := *msg
		filtered.Messages = make([]placestream.ChatDefs_MessageView, 0, len(msg.Messages))
		for _, entry := range msg.Messages {
			if view := a.ATSync.FilterChatMessage(ctx, streamer, &entry); view != nil {
				filtered.Messages = append(filtered.Messages, *view)
			}
		}
		filtered.PinnedComment = nil
		if pin := a.filterChatDelivery(ctx, streamer, msg.PinnedComment); pin != nil {
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
		filtered := a.ATSync.FilterChatMessage(ctx, streamer, &msg)
		if filtered == nil {
			return nil
		}
		return filtered
	case *placestream.ChatDefs_MessageView:
		filtered := a.ATSync.FilterChatMessage(ctx, streamer, msg)
		if filtered == nil {
			return nil
		}
		return filtered
	case placestream.ChatDefs_PinnedRecordView:
		return a.filterChatDelivery(ctx, streamer, &msg)
	case *placestream.ChatDefs_PinnedRecordView:
		if msg == nil {
			return nil
		}
		filtered := *msg
		if msg.Message != nil {
			filtered.Message = a.ATSync.FilterChatMessage(ctx, streamer, msg.Message)
			if filtered.Message == nil {
				return nil
			}
		}
		return &filtered
	}
	return message
}
