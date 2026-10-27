package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/streamplace/oatproxy/pkg/oatproxy"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/js/app"
	"stream.place/streamplace/js/web"
	"stream.place/streamplace/pkg/config"
)

func TestBundledWebFrontend(t *testing.T) {
	files, err := web.Files()
	require.NoError(t, err)
	index, err := fs.ReadFile(files, "index.html")
	require.NoError(t, err)
	module := regexp.MustCompile(`<script[^>]+type="module"[^>]+src="([^"]+)"`).FindSubmatch(index)
	require.Len(t, module, 2, "the web build must contain its browser entrypoint")
	script := string(module[1])
	asset, err := fs.ReadFile(files, strings.TrimPrefix(script, "/"))
	require.NoError(t, err)
	cli := &config.CLI{Build: &config.BuildFlags{}}
	updater, err := PrepareUpdater(cli)
	require.NoError(t, err)
	mimes, err := updater.GetMimes()
	require.NoError(t, err)
	a := &StreamplaceAPI{
		CLI:     cli,
		Updater: updater,
		Mimes:   mimes,
		op:      &oatproxy.OATProxy{Echo: echo.New()},
	}
	handler, err := a.Handler(t.Context())
	require.NoError(t, err)

	for _, cookie := range []string{"", "sp_web_beta=0", "sp_web_beta=1", "sp_web_beta=garbage"} {
		t.Run(cookie, func(t *testing.T) {
			for _, path := range []string{"/", "/settings/account"} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Cookie", cookie)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				require.Equal(t, http.StatusOK, rec.Code, path)
				require.Contains(t, rec.Body.String(), script, path)
				require.NotContains(t, rec.Body.String(), "_expo/static/js/web", path)
			}
		})
	}

	t.Run("browser asset", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, script, nil))
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, asset, rec.Body.Bytes())
	})

	for _, path := range []string{"/assets/missing.js", "/_expo/static/js/web/old-hash.js"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			require.Equal(t, http.StatusNotFound, rec.Code)
			require.NotContains(t, rec.Body.String(), script)
		})
	}

	nativeFiles, err := app.Files()
	require.NoError(t, err)
	for _, platform := range []string{IOS, ANDROID} {
		t.Run(platform+" update", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/manifest", nil)
			req.Header.Set("Expo-Platform", platform)
			req.Header.Set("Expo-Runtime-Version", updater.RuntimeVersion)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)
			var manifest UpdateManifest
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &manifest))
			require.NotEmpty(t, manifest.Assets)
			for _, asset := range []UpdateAsset{manifest.LaunchAsset, manifest.Assets[0]} {
				u, err := url.Parse(asset.URL)
				require.NoError(t, err)
				want, err := fs.ReadFile(nativeFiles, strings.TrimPrefix(u.Path, "/"))
				require.NoError(t, err)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, u.Path, nil))
				require.Equal(t, http.StatusOK, rec.Code, u.Path)
				require.Equal(t, asset.ContentType, rec.Header().Get("Content-Type"), u.Path)
				require.Equal(t, want, rec.Body.Bytes(), u.Path)
			}
		})
	}
}

func TestNativeAssetsSurviveWebCutover(t *testing.T) {
	cli := &config.CLI{Build: &config.BuildFlags{}}
	updater, err := PrepareUpdater(cli)
	require.NoError(t, err)
	files, err := app.Files()
	require.NoError(t, err)
	for _, platform := range []string{IOS, ANDROID} {
		t.Run(platform, func(t *testing.T) {
			manifest, err := updater.GetManifest(platform, updater.RuntimeVersion, "https://example.com")
			require.NoError(t, err)
			require.NotEmpty(t, manifest.LaunchAsset.Path)
			for _, asset := range append(manifest.Assets, manifest.LaunchAsset) {
				data, err := fs.ReadFile(files, asset.Path)
				require.NoError(t, err, asset.Path)
				require.NotEmpty(t, data, asset.Path)
			}
		})
	}
	for _, name := range []string{"favicon.png", "favicon.ico", "brand/notification-icon.png", "linkbanner.png"} {
		data, err := fs.ReadFile(files, name)
		require.NoError(t, err, name)
		require.NotEmpty(t, data, name)
	}
	assets, err := app.Assets()
	require.NoError(t, err)
	for _, name := range []string{"fonts/Geist-Regular.ttf", "fonts/GeistMono-Regular.ttf", "fonts/Geist-SemiBold.ttf"} {
		data, err := fs.ReadFile(assets, name)
		require.NoError(t, err, name)
		require.NotEmpty(t, data, name)
	}
}
