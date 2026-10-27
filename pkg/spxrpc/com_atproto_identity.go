package spxrpc

import (
	"context"
	"errors"
	"net/http"

	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/labstack/echo/v4"
	glex "github.com/streamplace/glex/runtime"
	"stream.place/streamplace/pkg/comatproto"
)

func (s *Server) handleComAtprotoIdentityResolveHandle(ctx context.Context, handle string) (*comatproto.IdentityResolveHandle_Output, error) {
	parsed, err := syntax.ParseHandle(handle)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "HandleNotFound")
	}
	ident, err := s.ATSync.IdentityDirectory().LookupHandle(ctx, parsed)
	if err != nil {
		if errors.Is(err, identity.ErrHandleNotFound) || errors.Is(err, identity.ErrHandleMismatch) ||
			errors.Is(err, identity.ErrHandleNotDeclared) || errors.Is(err, identity.ErrDIDNotFound) ||
			errors.Is(err, identity.ErrInvalidHandle) || errors.Is(err, identity.ErrHandleReservedTLD) {
			return nil, echo.NewHTTPError(http.StatusNotFound, "HandleNotFound")
		}
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, "IdentityUnavailable").SetInternal(err)
	}
	return &comatproto.IdentityResolveHandle_Output{Did: ident.DID.String()}, nil
}

func (s *Server) handleComAtprotoIdentityRefreshIdentity(ctx context.Context, body *comatproto.IdentityRefreshIdentity_Input) (*comatproto.IdentityDefs_IdentityInfo, error) {
	ident, err := s.ATSync.RefreshIdentity(ctx, body.Identifier)
	if err != nil {
		return nil, err
	}
	didDoc, err := glex.Unknown(ident.DIDDocument())
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "failed to encode DID document")
	}
	return &comatproto.IdentityDefs_IdentityInfo{
		Did:    ident.DID.String(),
		Handle: ident.Handle.String(),
		DidDoc: didDoc,
	}, nil
}

func (s *Server) handleComAtprotoIdentityUpdateHandle(ctx context.Context, body *comatproto.IdentityUpdateHandle_Input) error {
	return echo.NewHTTPError(501, "not implemented")
}
