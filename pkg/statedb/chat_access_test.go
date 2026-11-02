package statedb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	glex "github.com/streamplace/glex/runtime"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/appbsky"
	"stream.place/streamplace/pkg/aqhttp"
	"stream.place/streamplace/pkg/placestream"
)

// Route the external avatar lookup to the same real loopback HTTP server.
type chatAccessHTTP struct{ target *url.URL }

func (transport chatAccessHTTP) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.URL.Scheme = transport.target.Scheme
	copy.URL.Host = transport.target.Host
	return http.DefaultTransport.RoundTrip(copy)
}

func TestQueuedChatTaskRechecksNodeRestriction(t *testing.T) {
	const author = "did:plc:queueauthor"
	delivered := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/xrpc/app.bsky.actor.getProfile" {
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(appbsky.ActorDefs_ProfileViewDetailed{Did: author, Handle: "author.test"}))
			return
		}
		delivered <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	original := aqhttp.Client
	aqhttp.Client = http.Client{Transport: chatAccessHTTP{target}, Timeout: time.Second}
	defer func() { aqhttp.Client = original }()
	WithAllDatabases(t, func(state *StatefulDB) {
		require.NoError(t, state.CreateWebhook(&Webhook{UserDID: author, URL: server.URL + "/webhook", Events: []byte(`["chat"]`), Active: true}))
		view := placestream.ChatDefs_MessageView{Author: appbsky.ActorDefs_ProfileViewBasic{Did: author, Handle: "author.test"}, Record: &glex.LexiconTypeDecoder{Val: &placestream.ChatMessage{Streamer: author, Text: "queued before restriction"}}}
		payload, err := json.Marshal(ChatTask{MessageView: view})
		require.NoError(t, err)
		task := &AppTask{Payload: payload}
		require.NoError(t, state.processChatMessageTask(context.Background(), task))
		select {
		case <-delivered:
		case <-time.After(2 * time.Second):
			t.Fatal("allowed chat did not reach the real webhook")
		}
		policy, err := access.NewSnapshot(access.Bootstrap{Authority: "did:web:node.test"}, 1, []access.Entry{{Subject: author, Capability: access.Chat, Effect: access.Deny}})
		require.NoError(t, err)
		state.Access = policy
		require.NoError(t, state.processChatMessageTask(context.Background(), task))
		select {
		case <-delivered:
			t.Fatal("a queued chat task escaped an active node restriction")
		case <-time.After(100 * time.Millisecond):
		}
	})
}
