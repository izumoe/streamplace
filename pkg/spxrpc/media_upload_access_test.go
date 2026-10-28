package spxrpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/streamplace/oatproxy/pkg/oatproxy"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/atproto"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/placestream"
)

func TestCreateUploadAccess(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	jwkKey, err := jwk.FromRaw(key)
	require.NoError(t, err)
	keyJSON, err := json.Marshal(jwkKey)
	require.NoError(t, err)

	const creator = "did:plc:streamer"
	for _, tc := range []struct {
		name    string
		did     string
		allowed []string
		banned  bool
		status  int
	}{
		{"login required even on an open node", "", nil, false, http.StatusUnauthorized},
		{"open node permits an authenticated account", creator, nil, false, http.StatusServiceUnavailable},
		{"operator allowlist permits the account", creator, []string{creator}, false, http.StatusServiceUnavailable},
		{"operator allowlist rejects another account", creator, []string{"did:plc:other"}, false, http.StatusForbidden},
		{"ban overrides operator allowance", creator, []string{creator}, true, http.StatusForbidden},
		{"open node still rejects an unlisted test key", "did:key:zTest", nil, false, http.StatusForbidden},
		{"operator may allow the test key", "did:key:zTest", []string{"did:key:zTest"}, false, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			db, err := m.(*model.DBModel).DB.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			if tc.banned {
				putLabel(t, m, tc.did, atproto.LabelTakedown)
			}
			s := &Server{model: m, cli: &config.CLI{AllowedStreams: tc.allowed}}
			ctx := context.Background()
			if tc.did != "" {
				ctx = context.WithValue(ctx, oatproxy.OATProxyContextKey, oatproxy.New(&oatproxy.Config{Host: "stream.example"}))
				ctx = context.WithValue(ctx, oatproxy.OAuthSessionContextKey, &oatproxy.OAuthSession{DID: tc.did, UpstreamDPoPPrivateJWK: string(keyJSON)})
			}
			_, err = s.handlePlaceStreamMediaCreateUpload(ctx, &placestream.MediaCreateUpload_Input{MimeType: "video/mp4"})
			var he *echo.HTTPError
			require.ErrorAs(t, err, &he)
			require.Equal(t, tc.status, he.Code)
			if tc.status == http.StatusServiceUnavailable {
				require.Equal(t, "upload manager not configured", he.Message, "permitted callers reach the upload service")
			}
		})
	}
}
