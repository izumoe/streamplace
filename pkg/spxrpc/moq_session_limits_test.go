package spxrpc

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/labstack/echo/v4"
	httpmetrics "github.com/slok/go-http-metrics/middleware"
	"github.com/streamplace/oatproxy/pkg/oatproxy"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
)

func TestMoqSessionLimitIsRegistered(t *testing.T) {
	cli := &config.CLI{DataDir: t.TempDir(), MoqSessionRateLimit: 1, MoqSessionRateBurst: 2}
	s, err := NewServer(t.Context(), cli, nil, nil, oatproxy.New(&oatproxy.Config{Host: "node.example"}), httpmetrics.New(httpmetrics.Config{}), nil, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	synctest.Test(t, func(t *testing.T) {
		request := func(method, path string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, path, nil)
			req.RemoteAddr = "203.0.113.1:1234"
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			return rec
		}
		// These requests reach validation; the third must stop before the handler.
		require.Equal(t, http.StatusBadRequest, request(http.MethodPost, "/api/moq/session").Code)
		require.Equal(t, http.StatusBadRequest, request(http.MethodPost, "/api/moq/session").Code)
		require.Equal(t, http.StatusTooManyRequests, request(http.MethodPost, "/api/moq/session").Code)
		require.Equal(t, http.StatusBadRequest, request(http.MethodGet, "/xrpc/place.stream.playback.getLivePlaylist").Code)
	})
}

func TestMoqSessionLimitCanBeDisabled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := echo.New()
		limit, err := moqSessionLimiter(&config.CLI{})
		require.NoError(t, err)
		e.POST("/session", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) }, limit)
		for range 200 {
			req := httptest.NewRequest(http.MethodPost, "/session", nil)
			req.RemoteAddr = "203.0.113.1:1234"
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			require.Equal(t, http.StatusNoContent, rec.Code)
		}
	})
}

func TestMoqSessionLimitRetainsUnrefilledBudgets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := echo.New()
		limit, err := moqSessionLimiter(&config.CLI{MoqSessionRateLimit: 1, MoqSessionRateBurst: 200})
		require.NoError(t, err)
		e.POST("/session", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) }, limit)
		request := func(remote string) int {
			req := httptest.NewRequest(http.MethodPost, "/session", nil)
			req.RemoteAddr = remote
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			return rec.Code
		}
		for range 200 {
			require.Equal(t, 204, request("203.0.113.1:1234"))
		}
		time.Sleep(181 * time.Second)
		require.Equal(t, 204, request("203.0.113.2:1234")) // triggers idle-store cleanup
		for range 181 {
			require.Equal(t, 204, request("203.0.113.1:1234"))
		}
		require.Equal(t, 429, request("203.0.113.1:1234"), "idle eviction must not reset a budget before it refills")
	})
}

func TestMoqSessionLimitDefaultsAndRefill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := echo.New()
		limit, err := moqSessionLimiter(&config.CLI{MoqSessionRateLimit: 20, MoqSessionRateBurst: 100})
		require.NoError(t, err)
		e.POST("/api/moq/session", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) }, limit)
		e.GET("/video", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
		request := func(path, method, remote string) *httptest.ResponseRecorder {
			t.Helper()
			req := httptest.NewRequest(method, path, nil)
			req.RemoteAddr = remote
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			return rec
		}
		for range 100 {
			require.Equal(t, http.StatusNoContent, request("/api/moq/session", http.MethodPost, "203.0.113.1:1234").Code)
		}
		denied := request("/api/moq/session", http.MethodPost, "203.0.113.1:9999")
		require.Equal(t, http.StatusTooManyRequests, denied.Code)
		require.Equal(t, "1", denied.Header().Get("Retry-After"))
		require.Equal(t, http.StatusNoContent, request("/video", http.MethodGet, "203.0.113.1:9999").Code, "playback has a separate request budget")
		require.Equal(t, http.StatusNoContent, request("/api/moq/session", http.MethodPost, "203.0.113.2:1234").Code)
		time.Sleep(50 * time.Millisecond)
		require.Equal(t, http.StatusNoContent, request("/api/moq/session", http.MethodPost, "203.0.113.1:1234").Code, "one token refills every 50 ms")
		require.Equal(t, http.StatusTooManyRequests, request("/api/moq/session", http.MethodPost, "203.0.113.1:1234").Code)
		// A hundred aligned heartbeats fit the default burst every ten seconds.
		time.Sleep(10 * time.Second)
		for range 100 {
			require.Equal(t, http.StatusNoContent, request("/api/moq/session", http.MethodPost, "203.0.113.1:1234").Code)
		}
	})
}

func TestMoqSessionLimitProxyTrust(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		proxies               []string
		remote, first, second string
		want                  int
	}{
		{"direct spoof", nil, "203.0.113.1:1234", "198.51.100.1", "198.51.100.2", 429},
		{"untrusted proxy", []string{"10.0.0.0/24"}, "192.0.2.1:1234", "198.51.100.1", "198.51.100.2", 429},
		{"private is not implicitly trusted", []string{"10.0.0.0/24"}, "10.99.0.1:1234", "198.51.100.1", "198.51.100.2", 429},
		{"trusted proxy separates clients", []string{"10.0.0.0/24"}, "10.0.0.1:1234", "198.51.100.1", "198.51.100.2", 204},
		{"trusted chain ignores spoofed prefix", []string{"10.0.0.0/24", "192.0.2.0/24"}, "10.0.0.1:1234", "198.51.100.1, 203.0.113.7, 192.0.2.10", "198.51.100.2, 203.0.113.7, 192.0.2.10", 429},
		{"malformed chain falls back to peer", []string{"10.0.0.0/24"}, "10.0.0.1:1234", "not-an-ip", "also-invalid", 429},
		{"ipv6 peers", nil, "[2001:db8::1]:1234", "198.51.100.1", "198.51.100.2", 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := echo.New()
				limit, err := moqSessionLimiter(&config.CLI{MoqSessionRateLimit: 1, MoqSessionRateBurst: 1, MoqSessionTrustedProxies: tc.proxies})
				require.NoError(t, err)
				e.POST("/session", func(c echo.Context) error { return c.NoContent(204) }, limit)
				request := func(forwarded string) int {
					req := httptest.NewRequest(http.MethodPost, "/session", nil)
					req.RemoteAddr = tc.remote
					req.Header.Set("X-Forwarded-For", forwarded)
					req.Header.Set("X-Real-IP", forwarded)
					rec := httptest.NewRecorder()
					e.ServeHTTP(rec, req)
					return rec.Code
				}
				require.Equal(t, 204, request(tc.first))
				require.Equal(t, tc.want, request(tc.second))
			})
		})
	}
}
