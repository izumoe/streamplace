package model

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm/logger"
	"stream.place/streamplace/pkg/placestream"
)

// TestMostRecentChatMessagesRetention: a node serves chat only inside its
// retention window, so a viewer arriving the next day lands on an empty chat
// instead of yesterday's conversation. The filter is a read, not a delete --
// the rows stay indexed -- and a zero cutoff still serves the full backlog.
func TestMostRecentChatMessagesRetention(t *testing.T) {
	ctx := context.Background()
	mod := indexedTestDB(t)

	now := time.Now().UTC()
	rec := &placestream.ChatMessage{
		LexiconTypeID: "place.stream.chat.message",
		Text:          "hello",
		CreatedAt:     now.Format(time.RFC3339),
		Streamer:      "did:plc:streamer",
	}
	var buf bytes.Buffer
	require.NoError(t, rec.MarshalCBOR(&buf))
	body := buf.Bytes()

	require.NoError(t, mod.CreateChatMessage(ctx, &ChatMessage{
		CID:             "bafyold",
		URI:             "at://did:plc:chatter/place.stream.chat.message/3lold",
		CreatedAt:       now.Add(-25 * time.Hour),
		ChatMessage:     &body,
		RepoDID:         "did:plc:chatter",
		StreamerRepoDID: "did:plc:streamer",
		IndexedAt:       &now,
	}))
	require.NoError(t, mod.CreateChatMessage(ctx, &ChatMessage{
		CID:             "bafyrecent",
		URI:             "at://did:plc:chatter/place.stream.chat.message/3lrecent",
		CreatedAt:       now.Add(-time.Hour),
		ChatMessage:     &body,
		RepoDID:         "did:plc:chatter",
		StreamerRepoDID: "did:plc:streamer",
		IndexedAt:       &now,
	}))

	all, err := mod.MostRecentChatMessages(ctx, "did:plc:streamer", time.Time{})
	require.NoError(t, err)
	require.Len(t, all, 2, "a zero cutoff serves the whole backlog")

	recent, err := mod.MostRecentChatMessages(ctx, "did:plc:streamer", now.Add(-24*time.Hour))
	require.NoError(t, err)
	require.Len(t, recent, 1, "a message older than the window is withheld")
	require.Equal(t,
		"at://did:plc:chatter/place.stream.chat.message/3lrecent", recent[0].Uri)

	require.Equal(t, int64(2), countRows(t, mod, &ChatMessage{}),
		"withholding history must not delete it")
}

func TestChatSnapshotReadsHonorCancellation(t *testing.T) {
	mod := indexedTestDB(t)
	mod.DB.Logger = logger.Discard
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		read func() error
	}{
		{"history", func() error { _, err := mod.MostRecentChatMessages(ctx, "did:plc:streamer", time.Time{}); return err }},
		{"message", func() error {
			_, err := mod.GetChatMessage(ctx, "at://did:plc:author/place.stream.chat.message/one")
			return err
		}},
		{"pin", func() error { _, err := mod.GetActivePinnedRecord(ctx, "did:plc:streamer"); return err }},
		{"chat profile", func() error { _, err := mod.GetChatProfile(ctx, "did:plc:author"); return err }},
		{"verification profile", func() error { _, err := mod.GetBskyProfile(ctx, "did:plc:author", false); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) { require.ErrorIs(t, tc.read(), context.Canceled) })
	}
}
