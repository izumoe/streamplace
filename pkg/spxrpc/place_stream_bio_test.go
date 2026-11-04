package spxrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/bluesky-social/indigo/atproto/atdata"
	"github.com/labstack/echo/v4"
	glex "github.com/streamplace/glex/runtime"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/atproto"
	"stream.place/streamplace/pkg/comatproto"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/devenv"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/placestream"
	"stream.place/streamplace/pkg/spid"
)

func TestBioGetPageModeration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		labelURI string
		label    string
		status   int
	}{
		{"public", "", "", http.StatusOK},
		{"hidden record", "at://did:plc:owner/place.stream.bio.page/self", atproto.LabelHide, http.StatusNotFound},
		{"banned account", testOwner, atproto.LabelTakedown, http.StatusNotFound},
		{"warning", "at://did:plc:owner/place.stream.bio.page/self", atproto.LabelWarn, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			var buf bytes.Buffer
			require.NoError(t, (&placestream.BioPage{CreatedAt: "2026-10-02T00:00:00Z"}).MarshalCBOR(&buf))
			record := buf.Bytes()
			require.NoError(t, m.CreateBioPage(context.Background(), &model.BioPage{RepoDID: testOwner, RepoRev: "3lq4mksr7i22a", Record: &record}))
			if tc.labelURI != "" {
				putLabel(t, m, tc.labelURI, tc.label)
			}
			s := &Server{model: m, ATSync: &atproto.ATProtoSynchronizer{Model: m}}
			page, err := s.handlePlaceStreamBioGetPage(context.Background(), testOwner)
			if tc.status == http.StatusOK {
				require.NoError(t, err)
				require.NotNil(t, page)
				return
			}
			var httpErr *echo.HTTPError
			require.ErrorAs(t, err, &httpErr)
			require.Equal(t, tc.status, httpErr.Code)
			require.Nil(t, page)
		})
	}
}

func TestBioGetPageIndexesUnchangedRepo(t *testing.T) {
	dev := devenv.WithDevEnv(t)
	ctx := t.Context()
	user := dev.CreateAccount(t)
	rkey := "self"
	_, err := comatproto.RepoCreateRecord(ctx, user.XRPC, &comatproto.RepoCreateRecord_Input{
		Repo: user.DID, Collection: "place.stream.bio.page", Rkey: &rkey,
		Record: &glex.LexiconTypeDecoder{Val: &placestream.BioPage{CreatedAt: time.Now().UTC().Format(time.RFC3339), Description: &placestream.BioPage_Description{Plaintext: "already published"}}},
	})
	require.NoError(t, err)
	head, err := comatproto.SyncGetLatestCommit(ctx, user.XRPC, user.DID)
	require.NoError(t, err)
	mod := newTestModel(t)
	require.NoError(t, mod.UpdateRepo(&model.Repo{DID: user.DID, Handle: user.Handle, PDS: dev.PDSURL, Version: head.Rev, BackfillDone: true}))
	server := &Server{model: mod, ATSync: &atproto.ATProtoSynchronizer{Model: mod, CLI: &config.CLI{PLCURL: dev.PLCURL}}}
	e := echo.New()
	e.Use(server.ErrorHandlingMiddleware())
	require.NoError(t, server.RegisterHandlersPlacestream(e))
	response := httptest.NewRecorder()
	e.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/xrpc/place.stream.bio.getPage?repo="+user.DID, nil))
	require.Equal(t, http.StatusOK, response.Code)
	var page placestream.BioPage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Equal(t, "already published", page.Description.Plaintext)
	stored, err := mod.GetRepo(user.DID)
	require.NoError(t, err)
	require.Equal(t, head.Rev, stored.Version, "bio reads leave the existing repo checkpoint intact")
	require.True(t, stored.BackfillDone)
}

func TestBioGetPageHTTP(t *testing.T) {
	previousVerbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "1"))
	t.Cleanup(func() { require.NoError(t, flag.Set("v", previousVerbosity)) })
	var logs bytes.Buffer
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(logger) })
	for _, tc := range []struct {
		name     string
		repo     string
		hasPage  bool
		labelURI string
		label    string
		status   int
	}{
		{"missing page", testOwner, false, "", "", http.StatusNotFound},
		{"hidden record", testOwner, true, "at://did:plc:owner/place.stream.bio.page/self", atproto.LabelHide, http.StatusNotFound},
		{"banned account", testOwner, true, testOwner, atproto.LabelTakedown, http.StatusNotFound},
		{"handle", "owner.test", true, "", "", http.StatusOK},
		{"hidden handle", "owner.test", true, "at://did:plc:owner/place.stream.bio.page/self", atproto.LabelHide, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			require.NoError(t, m.UpdateRepo(&model.Repo{DID: testOwner, Handle: "owner.test", Version: "3lrev00000000"}))
			if tc.hasPage {
				var buf bytes.Buffer
				require.NoError(t, (&placestream.BioPage{CreatedAt: "2026-10-02T00:00:00Z"}).MarshalCBOR(&buf))
				record := buf.Bytes()
				require.NoError(t, m.CreateBioPage(context.Background(), &model.BioPage{RepoDID: testOwner, RepoRev: "3lq4mksr7i22a", Record: &record}))
			} else {
				require.NoError(t, m.DeleteBioPage(t.Context(), testOwner, "3lq4mksr7i22a"))
			}
			if tc.labelURI != "" {
				putLabel(t, m, tc.labelURI, tc.label)
			}
			s := &Server{model: m, ATSync: &atproto.ATProtoSynchronizer{Model: m}}
			e := echo.New()
			e.Use(s.ErrorHandlingMiddleware())
			require.NoError(t, s.RegisterHandlersPlacestream(e))
			response := httptest.NewRecorder()
			logs.Reset()
			e.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/xrpc/place.stream.bio.getPage?repo="+url.QueryEscape(tc.repo), nil))
			require.Equal(t, tc.status, response.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			if tc.status == http.StatusNotFound {
				require.Equal(t, "BioNotFound", body["error"])
				require.Contains(t, logs.String(), "http error")
			} else {
				require.Equal(t, "place.stream.bio.page", body["$type"])
			}
		})
	}
}

func TestBioGetPagePreservesUnknownUnionContent(t *testing.T) {
	link, err := spid.GetCID(&placestream.BioPage{CreatedAt: "2026-11-03T00:00:00Z"})
	require.NoError(t, err)
	payload := map[string]any{"bytes": map[string]any{"$bytes": "AAEC"}, "link": map[string]any{"$link": link.String()}}
	for name, fields := range map[string]map[string]any{
		"layout": {"layout": map[string]any{"$type": "future.layout", "payload": payload}},
		"block":  {"layout": map[string]any{"$type": "place.stream.bio.layouts.panels", "panels": []any{map[string]any{"blocks": []any{map[string]any{"block": map[string]any{"$type": "future.block", "payload": payload}}}}}}},
		"facet":  {"description": map[string]any{"plaintext": "hello", "facets": []any{map[string]any{"index": map[string]any{"byteStart": 0, "byteEnd": 5}, "features": []any{map[string]any{"$type": "future.annotation", "payload": payload}}}}}},
	} {
		t.Run(name, func(t *testing.T) {
			fields["$type"], fields["createdAt"] = "place.stream.bio.page", "2026-11-03T00:00:00Z"
			encoded, err := json.Marshal(fields)
			require.NoError(t, err)
			record, err := atdata.UnmarshalJSON(encoded)
			require.NoError(t, err)
			raw, err := atdata.MarshalCBOR(record)
			require.NoError(t, err)
			mod := newTestModel(t)
			require.NoError(t, mod.CreateBioPage(t.Context(), &model.BioPage{RepoDID: testOwner, RepoRev: "3lq4mksr7i22a", Record: &raw}))
			server := &Server{model: mod, ATSync: &atproto.ATProtoSynchronizer{Model: mod}}
			e := echo.New()
			e.Use(server.ErrorHandlingMiddleware())
			require.NoError(t, server.RegisterHandlersPlacestream(e))
			response := httptest.NewRecorder()
			e.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/xrpc/place.stream.bio.getPage?repo="+testOwner, nil))
			require.Equal(t, http.StatusOK, response.Code)
			var expected, actual map[string]any
			require.NoError(t, json.Unmarshal(encoded, &expected))
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &actual))
			if name == "facet" {
				expectedFacet := expected["description"].(map[string]any)["facets"].([]any)[0].(map[string]any)
				actualFacet := actual["description"].(map[string]any)["facets"].([]any)[0].(map[string]any)
				require.Equal(t, expectedFacet["features"], actualFacet["features"])
				require.Equal(t, expectedFacet["index"], actualFacet["index"])
				require.Equal(t, expected["description"].(map[string]any)["plaintext"], actual["description"].(map[string]any)["plaintext"])
			} else {
				require.Equal(t, expected, actual, "unknown variants and AT Protocol bytes/links must survive the indexed CBOR to JSON view")
			}
		})
	}
}
