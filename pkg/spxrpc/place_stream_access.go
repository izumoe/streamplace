package spxrpc

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/labstack/echo/v4"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/accessctl"
	"stream.place/streamplace/pkg/placestream"
)

func (s *Server) SetAccessController(controller *accessctl.Controller) { s.Access = controller }

func accessRPCError(status int, name, message string) error {
	return echo.NewHTTPError(status, map[string]string{"error": name, "message": message})
}

func (s *Server) requireAccessAdministrator(ctx context.Context) error {
	caller := GetCaller(ctx)
	if caller == nil || caller.DID == "" {
		return accessRPCError(http.StatusUnauthorized, "Unauthorized", "Sign in to manage node access.")
	}
	if s.Access == nil {
		return accessRPCError(http.StatusServiceUnavailable, "Unavailable", "Node access is unavailable.")
	}
	if !s.Access.Check(caller.DID, access.Admin).Allowed {
		return accessRPCError(http.StatusForbidden, "Forbidden", "Node administrator access is required.")
	}
	return nil
}

func (s *Server) handlePlaceStreamAccessGetStatus(ctx context.Context, subject string) (*placestream.AccessGetStatus_Output, error) {
	if s.Access == nil {
		return nil, accessRPCError(http.StatusServiceUnavailable, "Unavailable", "Node access is unavailable.")
	}
	did := ""
	if caller := GetCaller(ctx); caller != nil {
		did = caller.DID
	}
	if subject != "" && subject != did && !s.Access.Check(did, access.Admin).Allowed {
		return nil, accessRPCError(http.StatusForbidden, "Forbidden", "Only administrators may inspect another account.")
	}
	if subject == "" {
		subject = did
	}
	if subject != "" {
		if _, err := syntax.ParseDID(subject); err != nil {
			return nil, accessRPCError(http.StatusBadRequest, "InvalidRequest", "Subject must be a DID.")
		}
	}
	snapshot := s.Access.Snapshot()
	out := &placestream.AccessGetStatus_Output{Revision: int64(snapshot.Revision()), Capabilities: make([]placestream.AccessDefs_Decision, 0, len(access.Capabilities))}
	if subject != "" {
		out.Subject = &subject
	}
	for _, capability := range access.Capabilities {
		decision := snapshot.Check(subject, capability)
		setting := snapshot.Mode(capability)
		defaultMode := string(setting.DefaultMode)
		out.Capabilities = append(out.Capabilities, placestream.AccessDefs_Decision{Capability: string(capability), Mode: string(setting.Mode), DefaultMode: &defaultMode, ModeSource: &setting.Source, Allowed: decision.Allowed, Reason: decision.Reason, Enforced: capability != access.Viewer})
	}
	return out, nil
}

func (s *Server) handlePlaceStreamAccessSetMode(ctx context.Context, input *placestream.AccessSetMode_Input) (*placestream.AccessSetMode_Output, error) {
	if err := s.requireAccessAdministrator(ctx); err != nil {
		return nil, err
	}
	revision, err := strconv.ParseUint(input.ExpectedRevision, 10, 64)
	if err != nil {
		return nil, accessRPCError(http.StatusBadRequest, "InvalidRequest", "Expected revision must be the current decimal policy revision.")
	}
	var mode *access.Mode
	if input.Mode != nil {
		value := access.Mode(*input.Mode)
		mode = &value
	}
	if err := s.Access.SetMode(ctx, access.Capability(input.Capability), mode, revision); err != nil {
		if errors.Is(err, access.ErrConflict) {
			return nil, accessRPCError(http.StatusConflict, "Conflict", "Node policy changed. Review the current settings before saving again.")
		}
		return nil, accessMutationError(err)
	}
	return &placestream.AccessSetMode_Output{Success: true}, nil
}

func accessGrantView(entry access.Entry, allowed bool) placestream.AccessDefs_Grant {
	out := placestream.AccessDefs_Grant{Id: entry.ID, Authority: entry.Authority, Subject: entry.Subject, Capability: string(entry.Capability), Effect: string(entry.Effect), Reason: entry.Reason, Source: entry.Source, EffectiveAllowed: allowed}
	if !entry.CreatedAt.IsZero() {
		value := entry.CreatedAt.UTC().Format(time.RFC3339Nano)
		out.CreatedAt = &value
	}
	if entry.ExpiresAt != nil {
		value := entry.ExpiresAt.UTC().Format(time.RFC3339Nano)
		out.ExpiresAt = &value
	}
	return out
}

func (s *Server) handlePlaceStreamAccessListGrants(ctx context.Context) (*placestream.AccessListGrants_Output, error) {
	if err := s.requireAccessAdministrator(ctx); err != nil {
		return nil, err
	}
	snapshot := s.Access.Snapshot()
	entries := snapshot.Entries()
	out := &placestream.AccessListGrants_Output{Revision: int64(snapshot.Revision()), Grants: make([]placestream.AccessDefs_Grant, 0, len(entries))}
	for _, entry := range entries {
		out.Grants = append(out.Grants, accessGrantView(entry, snapshot.Check(entry.Subject, entry.Capability).Allowed))
	}
	return out, nil
}

func accessMutationError(err error) error {
	switch {
	case errors.Is(err, access.ErrInvalid):
		return accessRPCError(http.StatusBadRequest, "InvalidRequest", err.Error())
	case errors.Is(err, access.ErrConflict):
		return accessRPCError(http.StatusConflict, "Conflict", "Revoke the existing entry before changing its reason or expiry.")
	case errors.Is(err, access.ErrNotFound):
		return accessRPCError(http.StatusNotFound, "NotFound", "This access entry no longer exists.")
	default:
		return accessRPCError(http.StatusServiceUnavailable, "Unavailable", "Could not update node access.")
	}
}

func (s *Server) handlePlaceStreamAccessCreateGrant(ctx context.Context, input *placestream.AccessCreateGrant_Input) (*placestream.AccessDefs_Grant, error) {
	if err := s.requireAccessAdministrator(ctx); err != nil {
		return nil, err
	}
	var expires *time.Time
	if input.ExpiresAt != nil {
		value, err := time.Parse(time.RFC3339Nano, *input.ExpiresAt)
		if err != nil {
			return nil, accessRPCError(http.StatusBadRequest, "InvalidRequest", "Expiry must be an RFC3339 timestamp.")
		}
		expires = &value
	}
	reason := ""
	if input.Reason != nil {
		reason = *input.Reason
	}
	entry, err := s.Access.Create(ctx, input.Subject, access.Capability(input.Capability), access.Effect(input.Effect), reason, expires)
	if err != nil {
		return nil, accessMutationError(err)
	}
	out := accessGrantView(entry, s.Access.Check(entry.Subject, entry.Capability).Allowed)
	return &out, nil
}

func (s *Server) handlePlaceStreamAccessDeleteGrant(ctx context.Context, input *placestream.AccessDeleteGrant_Input) (*placestream.AccessDeleteGrant_Output, error) {
	if err := s.requireAccessAdministrator(ctx); err != nil {
		return nil, err
	}
	if err := s.Access.Delete(ctx, input.Id); err != nil {
		return nil, accessMutationError(err)
	}
	return &placestream.AccessDeleteGrant_Output{Success: true}, nil
}
