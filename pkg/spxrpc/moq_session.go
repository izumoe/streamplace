package spxrpc

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/psession"
)

// HandleMoqSession maintains an app-reported lease, not proof of a relay
// subscription. Only the signed session ID is stable across renewals.
func (s *Server) HandleMoqSession(c echo.Context) error {
	ctx := c.Request().Context()
	did, err := s.resolveStreamer(ctx, c.QueryParam("streamer"))
	if err != nil {
		return err
	}
	var body struct {
		SID    string `json:"sid"`
		Closed bool   `json:"closed"`
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 2048)
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "InvalidSession")
	}
	if body.Closed {
		sess, err := s.verifySession(ctx, body.SID, did)
		if err != nil {
			return err
		}
		s.mm.CloseMoqSession(did, sess.ID)
		return c.NoContent(http.StatusNoContent)
	}
	if banned, err := s.accountBanned(did); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError)
	} else if banned {
		return echo.NewHTTPError(http.StatusForbidden, "StreamUnavailable")
	}
	w, published := s.mm.GetLiveWindow(did)
	if w == nil {
		return echo.NewHTTPError(http.StatusNotFound, "StreamNotLive")
	}
	sess, err := s.resolveSession(ctx, body.SID, did, false)
	if err != nil {
		return err
	}
	if !published && !s.cli.WideOpen && sess.Scope != psession.ScopeOwner {
		return echo.NewHTTPError(http.StatusNotFound, "StreamNotLive")
	}
	s.mm.TouchMoqSession(did, sess.ID)
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]string{"sid": sess.SID})
}

// moqSessionLimiter bounds analytics work independently of media requests.
func moqSessionLimiter(cli *config.CLI) (echo.MiddlewareFunc, error) {
	options := []echo.TrustOption{echo.TrustLoopback(false), echo.TrustLinkLocal(false), echo.TrustPrivateNet(false)}
	for _, cidr := range cli.MoqSessionTrustedProxies {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid MoQ session trusted proxy %q: %w", cidr, err)
		}
		options = append(options, echo.TrustIPRange(network))
	}
	if cli.MoqSessionRateLimit == 0 {
		return func(next echo.HandlerFunc) echo.HandlerFunc { return next }, nil
	}
	extractIP := echo.ExtractIPDirect()
	if len(cli.MoqSessionTrustedProxies) > 0 {
		extractIP = echo.ExtractIPFromXFFHeader(options...)
	}
	// Eviction must not grant a fresh burst before the old budget can refill.
	expiresIn := middleware.DefaultRateLimiterMemoryStoreConfig.ExpiresIn
	refillNanos := math.Ceil(float64(cli.MoqSessionRateBurst) / float64(cli.MoqSessionRateLimit) * float64(time.Second))
	if refillNanos >= float64(math.MaxInt64) {
		expiresIn = time.Duration(math.MaxInt64)
	} else {
		expiresIn = max(expiresIn, time.Duration(refillNanos))
	}
	return middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(middleware.RateLimiterMemoryStoreConfig{
			Rate: rate.Limit(cli.MoqSessionRateLimit), Burst: cli.MoqSessionRateBurst, ExpiresIn: expiresIn,
		}),
		IdentifierExtractor: func(c echo.Context) (string, error) { return extractIP(c.Request()), nil },
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			c.Response().Header().Set("Retry-After", "1")
			return echo.NewHTTPError(http.StatusTooManyRequests, "rate limit exceeded")
		},
	}), nil
}
