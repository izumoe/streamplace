package spxrpc

import (
	"net/http"

	"github.com/labstack/echo/v4"
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
