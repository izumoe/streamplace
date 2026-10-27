package spxrpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/atproto"
	"stream.place/streamplace/pkg/devenv"
)

func TestResolveHandleUsesNodeIdentityDirectory(t *testing.T) {
	dev := devenv.WithDevEnv(t)
	account := dev.CreateAccount(t)
	s := &Server{ATSync: &atproto.ATProtoSynchronizer{PLCDirectory: dev.TestDirectory()}}
	out, err := s.handleComAtprotoIdentityResolveHandle(t.Context(), account.Handle)
	require.NoError(t, err)
	require.Equal(t, account.DID, out.Did)

	for _, handle := range []string{"", "missing.test"} {
		_, err := s.handleComAtprotoIdentityResolveHandle(context.Background(), handle)
		var httpErr *echo.HTTPError
		require.ErrorAs(t, err, &httpErr)
		if handle == "" {
			require.Equal(t, http.StatusBadRequest, httpErr.Code)
		} else {
			require.Equal(t, http.StatusNotFound, httpErr.Code)
		}
	}

	failedPLC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(failedPLC.Close)
	directory := dev.TestDirectory().(*identity.BaseDirectory)
	directory.PLCURL = failedPLC.URL
	s.ATSync = &atproto.ATProtoSynchronizer{PLCDirectory: directory}
	_, err = s.handleComAtprotoIdentityResolveHandle(t.Context(), account.Handle)
	var httpErr *echo.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusServiceUnavailable, httpErr.Code)
}
