package spxrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bluesky-social/indigo/xrpc"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestMakeUnauthenticatedRequest(t *testing.T) {
	previousVerbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "1"))
	t.Cleanup(func() { require.NoError(t, flag.Set("v", previousVerbosity)) })
	var logs bytes.Buffer
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(logger) })
	for _, tc := range []struct {
		name        string
		status      int
		body        string
		wantStatus  int
		wantError   string
		wantMessage string
	}{
		{"record", http.StatusOK, `{"uri":"at://did:plc:owner/place.stream.bio.page/self"}`, http.StatusOK, "", ""},
		{"missing record", http.StatusBadRequest, `{"error":"RecordNotFound","message":"Record not found"}`, http.StatusBadRequest, "RecordNotFound", "Record not found"},
		{"forbidden", http.StatusForbidden, `{"error":"Forbidden","message":"Permission denied"}`, http.StatusForbidden, "Forbidden", "Permission denied"},
		{"upstream failure", http.StatusInternalServerError, `{"error":"UpstreamFailure","message":"Try later"}`, http.StatusInternalServerError, "UpstreamFailure", "Try later"},
		{"rate limited", http.StatusTooManyRequests, `{"error":"RateLimitExceeded","message":"Try later"}`, http.StatusTooManyRequests, "RateLimitExceeded", "Try later"},
		{"malformed error", http.StatusBadGateway, `unavailable`, http.StatusBadGateway, "", ""},
		{"malformed record", http.StatusOK, `{`, http.StatusInternalServerError, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "/xrpc/com.atproto.repo.getRecord", r.URL.Path)
				require.Equal(t, "did:plc:owner", r.URL.Query().Get("repo"))
				require.Equal(t, "place.stream.bio.page", r.URL.Query().Get("collection"))
				require.Equal(t, "self", r.URL.Query().Get("rkey"))
				require.Equal(t, "spaces & punctuation", r.URL.Query().Get("cursor"))
				require.Empty(t, r.Header.Get("Authorization"))
				require.Equal(t, "true", r.Header.Get(repoProxyHeader))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, err := w.Write([]byte(tc.body))
				require.NoError(t, err)
			}))
			defer upstream.Close()
			s := &Server{}
			e := echo.New()
			e.Use(s.ErrorHandlingMiddleware())
			e.GET("/xrpc/com.atproto.repo.getRecord", func(c echo.Context) error {
				var record map[string]any
				err := makeUnauthenticatedRequest(c.Request().Context(), upstream.URL, "com.atproto.repo.getRecord", map[string]any{
					"repo": "did:plc:owner", "collection": "place.stream.bio.page", "rkey": "self", "cursor": "spaces & punctuation",
				}, &record)
				if err != nil {
					return err
				}
				return c.JSON(http.StatusOK, record)
			})
			response := httptest.NewRecorder()
			logs.Reset()
			e.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/xrpc/com.atproto.repo.getRecord", nil))
			require.Equal(t, tc.wantStatus, response.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			if tc.wantError != "" {
				require.Equal(t, tc.wantError, body["error"])
				require.Equal(t, tc.wantMessage, body["message"])
			} else if tc.wantStatus != http.StatusOK {
				require.NotContains(t, body, "error")
				require.NotEmpty(t, body["message"])
			} else {
				require.Equal(t, "at://did:plc:owner/place.stream.bio.page/self", body["uri"])
			}
			if tc.wantStatus != http.StatusOK {
				require.Contains(t, logs.String(), "error")
			}
		})
	}
}

func TestMakeUnauthenticatedRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	var record map[string]any
	err := makeUnauthenticatedRequest(ctx, upstream.URL, "com.atproto.repo.getRecord", nil, &record)
	require.ErrorIs(t, err, context.Canceled)
}

func TestErrorHandlingMiddlewareUpstreamProcedure(t *testing.T) {
	previousVerbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "1"))
	t.Cleanup(func() { require.NoError(t, flag.Set("v", previousVerbosity)) })
	var logs bytes.Buffer
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(logger) })
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		wantError string
	}{
		{"conflict", http.StatusBadRequest, `{"error":"InvalidSwap","message":"Record changed"}`, "InvalidSwap"},
		{"forbidden", http.StatusForbidden, `{"error":"Forbidden","message":"Permission denied"}`, "Forbidden"},
		{"upstream failure", http.StatusInternalServerError, `{"error":"UpstreamFailure","message":"Try later"}`, "UpstreamFailure"},
		{"malformed failure", http.StatusInternalServerError, `unavailable`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "/xrpc/com.atproto.repo.putRecord", r.URL.Path)
				var record map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&record))
				require.Equal(t, "self", record["rkey"])
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, err := w.Write([]byte(tc.body))
				require.NoError(t, err)
			}))
			defer upstream.Close()
			client := xrpc.Client{Host: upstream.URL, Client: upstream.Client()}
			server := &Server{}
			e := echo.New()
			e.Use(server.ErrorHandlingMiddleware())
			e.POST("/write", func(c echo.Context) error {
				var out map[string]any
				err := client.Do(c.Request().Context(), xrpc.Procedure, "application/json", "com.atproto.repo.putRecord", nil, map[string]any{"rkey": "self"}, &out)
				return fmt.Errorf("failed to proxy write: %w", err)
			})
			response := httptest.NewRecorder()
			logs.Reset()
			e.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/write", nil))
			require.Equal(t, tc.status, response.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			if tc.wantError != "" {
				require.Equal(t, tc.wantError, body["error"])
				var expected map[string]any
				require.NoError(t, json.Unmarshal([]byte(tc.body), &expected))
				require.Equal(t, expected["message"], body["message"])
			} else {
				require.NotContains(t, body, "error")
				require.NotEmpty(t, body["message"])
			}
			require.Contains(t, logs.String(), "error")
		})
	}
}

func TestErrorHandlingMiddlewarePreservesExplicitHTTPError(t *testing.T) {
	previousVerbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "1"))
	t.Cleanup(func() { require.NoError(t, flag.Set("v", previousVerbosity)) })
	var logs bytes.Buffer
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(logger) })
	server := &Server{}
	handler := server.ErrorHandlingMiddleware()(func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "IdentityUnavailable").SetInternal(&xrpc.Error{
			StatusCode: http.StatusNotFound,
			Wrapped:    &xrpc.XRPCError{ErrStr: "HandleNotFound", Message: "Handle not found"},
		})
	})
	e := echo.New()
	ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/identity", nil), httptest.NewRecorder())
	err := handler(ctx)
	var httpError *echo.HTTPError
	require.ErrorAs(t, err, &httpError)
	require.Equal(t, http.StatusServiceUnavailable, httpError.Code)
	require.Equal(t, "IdentityUnavailable", httpError.Message)
	require.Contains(t, logs.String(), "code=503")
}
