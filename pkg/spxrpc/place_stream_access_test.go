package spxrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/accessctl"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/statedb"
)

const accessAdmin = "did:plc:nodeadmin"
const accessPublisher = "did:plc:publisher"

func accessTestServer(t *testing.T) (*Server, *echo.Echo) {
	t.Helper()
	db, err := statedb.MakeDB(t.Context(), &config.CLI{DBURL: ":memory:"}, nil, nil)
	require.NoError(t, err)
	sqlDB, err := db.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	ctl, err := accessctl.New(t.Context(), access.Bootstrap{
		Authority: "did:web:node.example",
		Modes:     map[access.Capability]access.Mode{access.Admin: access.Allowlist, access.Viewer: access.Open, access.Chat: access.Open, access.Streamer: access.Allowlist, access.Syndicate: access.Off, access.VOD: access.Allowlist},
		Entries:   []access.Entry{{Subject: accessAdmin, Capability: access.Admin, Effect: access.Allow, Source: access.Configuration}},
	}, db, nil)
	require.NoError(t, err)
	s := &Server{}
	s.SetAccessController(ctl)
	e := echo.New()
	require.NoError(t, s.RegisterHandlersPlacestream(e))
	return s, e
}

func accessRequest(t *testing.T, e *echo.Echo, did, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, "/xrpc/place.stream.access."+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	// A bare bearer string does not become a verified caller.
	req.Header.Set("Authorization", "Bearer "+accessAdmin)
	if did != "" {
		req = req.WithContext(context.WithValue(req.Context(), pdsAuthContextKey, did))
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestNodeAccessRPCRequiresVerifiedAdministrator(t *testing.T) {
	_, e := accessTestServer(t)
	for _, did := range []string{"", accessPublisher} {
		for _, endpoint := range []struct {
			method, name string
			input        any
		}{
			{http.MethodGet, "listGrants", nil},
			{http.MethodPost, "createGrant", map[string]any{"subject": accessPublisher, "capability": "streamer", "effect": "allow"}},
			{http.MethodPost, "deleteGrant", map[string]any{"id": "not-an-entry"}},
			{http.MethodPost, "setMode", map[string]any{"capability": "streamer", "mode": "open", "expectedRevision": "0"}},
		} {
			rec := accessRequest(t, e, did, endpoint.method, endpoint.name, endpoint.input)
			want := http.StatusForbidden
			if did == "" {
				want = http.StatusUnauthorized
			}
			require.Equal(t, want, rec.Code, rec.Body.String())
		}
	}
	rec := accessRequest(t, e, accessAdmin, http.MethodGet, "listGrants", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"source":"configuration"`)
}

func TestNodeAccessRPCStatusPrivacyAndEffectiveDenials(t *testing.T) {
	_, e := accessTestServer(t)
	rec := accessRequest(t, e, "", http.MethodGet, "getStatus", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var status struct {
		Subject      *string
		Capabilities []struct {
			Capability        string
			Allowed, Enforced bool
		}
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	require.Nil(t, status.Subject)
	enforcement := map[string]bool{}
	for _, cap := range status.Capabilities {
		enforcement[cap.Capability] = cap.Enforced
		if cap.Capability == "viewer" {
			require.True(t, cap.Allowed)
			require.False(t, cap.Enforced)
		}
		if cap.Capability == "chat" || cap.Capability == "streamer" {
			require.False(t, cap.Allowed)
			require.True(t, cap.Enforced)
		}
	}
	require.Equal(t, map[string]bool{
		"admin": true, "chat": true, "streamer": true,
		"syndicate": true, "vod": true, "viewer": false,
	}, enforcement)
	rec = accessRequest(t, e, accessPublisher, http.MethodGet, "getStatus?subject="+accessAdmin, nil)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = accessRequest(t, e, accessAdmin, http.MethodGet, "getStatus?subject="+accessPublisher, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), accessPublisher)
}

func TestNodeAccessRPCExactRevocationAndConflict(t *testing.T) {
	s, e := accessTestServer(t)
	create := func(effect, reason string) map[string]any {
		rec := accessRequest(t, e, accessAdmin, http.MethodPost, "createGrant", map[string]any{"subject": accessPublisher, "capability": "streamer", "effect": effect, "reason": reason})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	allow := create("allow", "approved")
	again := create("allow", "approved")
	require.Equal(t, allow["id"], again["id"], "retry must not create another entry")
	deny := create("deny", "paused")
	require.NotEqual(t, allow["id"], deny["id"])
	require.False(t, s.Access.Check(accessPublisher, access.Streamer).Allowed)
	rec := accessRequest(t, e, accessAdmin, http.MethodPost, "createGrant", map[string]any{"subject": accessPublisher, "capability": "streamer", "effect": "allow", "reason": "changed without revoking"})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	rec = accessRequest(t, e, accessAdmin, http.MethodPost, "deleteGrant", map[string]any{"id": "stale-entry"})
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.False(t, s.Access.Check(accessPublisher, access.Streamer).Allowed)
	rec = accessRequest(t, e, accessAdmin, http.MethodPost, "deleteGrant", map[string]any{"id": deny["id"]})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, s.Access.Check(accessPublisher, access.Streamer).Allowed, "unban retains the independent allowance")
	rec = accessRequest(t, e, accessAdmin, http.MethodPost, "deleteGrant", map[string]any{"id": allow["id"]})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.False(t, s.Access.Check(accessPublisher, access.Streamer).Allowed)
}

func TestNodeAccessRPCRejectsUnsupportedAndExpiredWrites(t *testing.T) {
	_, e := accessTestServer(t)
	for _, capability := range []string{"admin", "viewer", "unknown"} {
		rec := accessRequest(t, e, accessAdmin, http.MethodPost, "createGrant", map[string]any{"subject": accessPublisher, "capability": capability, "effect": "allow"})
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	for _, expires := range []string{"not-a-date", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)} {
		rec := accessRequest(t, e, accessAdmin, http.MethodPost, "createGrant", map[string]any{"subject": accessPublisher, "capability": "chat", "effect": "deny", "expiresAt": expires})
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	rec := accessRequest(t, e, accessAdmin, http.MethodPost, "createGrant", map[string]any{"subject": accessPublisher, "capability": "chat", "effect": "deny", "expiresAt": future})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), future)
}

func TestNodeAccessRPCPolicyMutationAndReset(t *testing.T) {
	s, e := accessTestServer(t)
	rec := accessRequest(t, e, accessAdmin, http.MethodPost, "setMode", map[string]any{"capability": "streamer", "mode": "open", "expectedRevision": "0"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, s.Access.Check(accessPublisher, access.Streamer).Allowed)
	rec = accessRequest(t, e, accessAdmin, http.MethodGet, "getStatus", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var status struct {
		Capabilities []struct {
			Capability, Mode, ModeSource, DefaultMode string
		}
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	for _, capability := range status.Capabilities {
		if capability.Capability == "streamer" {
			require.Equal(t, "open", capability.Mode)
			require.Equal(t, "stored", capability.ModeSource)
			require.Equal(t, "allowlist", capability.DefaultMode)
		}
	}
	rec = accessRequest(t, e, accessAdmin, http.MethodPost, "setMode", map[string]any{"capability": "chat", "mode": "off", "expectedRevision": "0"})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.True(t, s.Access.Check(accessPublisher, access.Chat).Allowed)
	for _, capability := range []string{"admin", "viewer", "unknown"} {
		rec = accessRequest(t, e, accessAdmin, http.MethodPost, "setMode", map[string]any{"capability": capability, "mode": "open", "expectedRevision": strconv.FormatUint(s.Access.Snapshot().Revision(), 10)})
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	rec = accessRequest(t, e, accessAdmin, http.MethodPost, "setMode", map[string]any{"capability": "streamer", "expectedRevision": strconv.FormatUint(s.Access.Snapshot().Revision(), 10)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.False(t, s.Access.Check(accessPublisher, access.Streamer).Allowed)
	require.True(t, s.Access.Check("", access.Viewer).Allowed)
	require.False(t, s.Access.Check(accessPublisher, access.Admin).Allowed)
	for _, revision := range []any{nil, "", "-1", "invalid", "18446744073709551616"} {
		rec = accessRequest(t, e, accessAdmin, http.MethodPost, "setMode", map[string]any{"capability": "streamer", "mode": "open", "expectedRevision": revision})
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestNodeAccessRPCManagesCarriageAndUploadApprovals(t *testing.T) {
	s, e := accessTestServer(t)
	for _, capability := range []access.Capability{access.Syndicate, access.VOD} {
		rec := accessRequest(t, e, accessAdmin, http.MethodPost, "setMode", map[string]any{
			"capability": capability, "mode": "allowlist", "expectedRevision": strconv.FormatUint(s.Access.Snapshot().Revision(), 10),
		})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rec = accessRequest(t, e, accessAdmin, http.MethodPost, "createGrant", map[string]any{"subject": accessPublisher, "capability": capability, "effect": "allow"})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.True(t, s.Access.Check(accessPublisher, capability).Allowed)
	}
}
