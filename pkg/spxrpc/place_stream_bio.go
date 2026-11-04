package spxrpc

import (
	"context"
	"fmt"
	"net/http"

	"github.com/bluesky-social/indigo/xrpc"
	"github.com/labstack/echo/v4"
	"stream.place/streamplace/pkg/atproto"
	"stream.place/streamplace/pkg/placestream"
)

func (s *Server) handlePlaceStreamBioGetPage(ctx context.Context, repo string) (*placestream.BioPage, error) {
	repo, err := s.resolveStreamer(ctx, repo)
	if err != nil {
		return nil, err
	}
	accountLabels, err := s.model.GetActiveLabels(repo)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "Failed to get bio page")
	}
	if atproto.IsBanned(accountLabels...) {
		return nil, echo.NewHTTPError(http.StatusNotFound, xrpc.XRPCError{ErrStr: "BioNotFound", Message: "Bio page not found"})
	}

	recordLabels, err := s.model.GetActiveLabels(fmt.Sprintf("at://%s/place.stream.bio.page/self", repo))
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "Failed to get bio page")
	}
	for _, label := range recordLabels {
		switch label.Val {
		case atproto.LabelHide, atproto.LabelTakedown, atproto.LabelSuspend, atproto.LabelDMCAViolation:
			return nil, echo.NewHTTPError(http.StatusNotFound, xrpc.XRPCError{ErrStr: "BioNotFound", Message: "Bio page not found"})
		}
	}

	if err := s.ATSync.SyncBioPage(ctx, repo); err != nil {
		return nil, fmt.Errorf("failed to sync bio page: %w", err)
	}
	bp, err := s.model.GetBioPage(ctx, repo)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "Failed to get bio page")
	}
	if bp == nil {
		return nil, echo.NewHTTPError(http.StatusNotFound, xrpc.XRPCError{ErrStr: "BioNotFound", Message: "Bio page not found"})
	}
	return bp.ToStreamplaceBioPage()
}
