package spxrpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/atproto"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/muxl"
	"stream.place/streamplace/pkg/psession"
)

func TestMoqSessionHTTP(t *testing.T) {
	ctx := t.Context()
	b := bus.NewBus()
	cli := &config.CLI{}
	mm, err := media.MakeMediaManager(ctx, cli, nil, nil, b, nil, nil)
	require.NoError(t, err)
	mod, err := model.MakeDB(":memory:")
	require.NoError(t, err)
	s := &Server{mm: mm, model: mod, cli: cli}
	key := []byte("0123456789abcdef0123456789abcdef")
	s.sessionKeyCache.Store(&key)
	e := echo.New()
	e.POST("/api/moq/session", s.HandleMoqSession)
	server := httptest.NewServer(e)
	defer server.Close()
	request := func(did, body string, status int) string {
		t.Helper()
		res, err := server.Client().Post(server.URL+"/api/moq/session?streamer="+did, "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer res.Body.Close()
		require.Equal(t, status, res.StatusCode)
		if status != http.StatusOK {
			return ""
		}
		var out struct {
			SID string `json:"sid"`
		}
		require.NoError(t, json.NewDecoder(res.Body).Decode(&out))
		return out.SID
	}
	const did = "did:plc:moq-http"
	request(did, `{}`, http.StatusNotFound)
	fixture, err := os.ReadFile("../../test/fixtures/h264-opus-frag.mp4")
	require.NoError(t, err)
	canonical, err := muxl.RunMuxlCanonicalize(ctx, fixture, nil)
	require.NoError(t, err)
	require.NoError(t, mm.FeedLiveRenditions(ctx, did, canonical, time.Now(), true))
	sid := request(did, `{}`, http.StatusOK)
	require.NotEmpty(t, sid)
	require.Equal(t, 1, b.GetViewerCount(did))
	require.Equal(t, sid, request(did, `{"sid":"`+sid+`"}`, http.StatusOK))
	require.Equal(t, 1, b.GetViewerCount(did), "repeated heartbeats count once")
	request(did, `{"sid":"forged"}`, http.StatusBadRequest)
	request(did, `{"sid":"`+strings.Repeat("x", 2048)+`"}`, http.StatusBadRequest)
	_, other := psession.Mint(key, "did:plc:other", psession.ScopePublic, time.Hour, time.Now())
	request(did, `{"sid":"`+other+`"}`, http.StatusBadRequest)
	request(did, `{"sid":"`+other+`","closed":true}`, http.StatusForbidden)
	request(did, `{"sid":"`+sid+`","closed":true}`, http.StatusNoContent)
	request(did, `{"sid":"`+sid+`","closed":true}`, http.StatusNoContent)
	require.Equal(t, 0, b.GetViewerCount(did))
	putLabel(t, mod, did, atproto.LabelTakedown)
	request(did, `{"sid":"`+sid+`"}`, http.StatusForbidden)
	const preview = "did:plc:moq-preview"
	require.NoError(t, mm.FeedLiveRenditions(ctx, preview, canonical, time.Now(), false))
	request(preview, `{}`, http.StatusNotFound)
	_, owner := psession.Mint(key, preview, psession.ScopeOwner, time.Hour, time.Now())
	request(preview, `{"sid":"`+owner+`"}`, http.StatusOK)
	request(preview, `{"sid":"`+owner+`","closed":true}`, http.StatusNoContent)
}
