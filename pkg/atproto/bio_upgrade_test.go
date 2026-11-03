package atproto

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bluesky-social/indigo/util"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/devenv"
	"stream.place/streamplace/pkg/model"
	"stream.place/streamplace/pkg/placestream"
)

func TestBioUpgradeIndexesUnchangedRepo(t *testing.T) {
	dev := devenv.WithDevEnv(t)
	for _, sweep := range []bool{false, true} {
		for _, present := range []bool{false, true} {
			name := "bio read"
			if sweep {
				name = "sweep"
			}
			if present {
				name += "/published"
			} else {
				name += "/absent"
			}
			t.Run(name, func(t *testing.T) {
				ctx := t.Context()
				atsync, mod := backfillTestSynchronizer(t, dev)
				user := dev.CreateAccount(t)
				db := mod.(*model.DBModel).DB
				if present {
					createBackfillRecord(t, user, "place.stream.bio.page", "self", &placestream.BioPage{CreatedAt: time.Now().UTC().Format(util.ISO8601)})
				}
				repo, err := atsync.SyncBlueskyRepoCached(ctx, user.DID)
				require.NoError(t, err)
				repo.BackfillDone = true
				require.NoError(t, mod.UpdateRepo(repo))
				// An upgraded database retains its completed repo rows, but has
				// never indexed this newly supported collection.
				require.NoError(t, db.Where("repo_did = ?", user.DID).Delete(&model.BioPage{}).Error)
				check := func() {
					t.Helper()
					if sweep {
						require.False(t, atsync.sweepCheck(ctx, &sweepProgress{}, func(sweepItem) {
							t.Error("an unchanged repo must not need a full repair")
						}, sweepStep{sweepItem: sweepItem{DID: user.DID, Handle: user.Handle, Check: true}}))
					} else {
						err := atsync.SyncBioPage(ctx, user.DID)
						require.NoError(t, err)
					}
				}
				if !sweep && present {
					require.NoError(t, db.Exec("CREATE TRIGGER reject_upgrade_bio BEFORE INSERT ON bio_pages BEGIN SELECT RAISE(ABORT, 'bio upgrade failed'); END").Error)
					err := atsync.SyncBioPage(ctx, user.DID)
					require.ErrorContains(t, err, "bio upgrade failed")
					require.NoError(t, db.Exec("DROP TRIGGER reject_upgrade_bio").Error)
				}
				check()
				var coverage model.BioPage
				require.NoError(t, db.Where("repo_did = ?", user.DID).First(&coverage).Error,
					"the unchanged snapshot must index either its bio or a durable absence")
				require.Equal(t, repo.Version, coverage.RepoRev)
				require.Equal(t, present, coverage.Record != nil)
				stored, err := mod.GetRepo(user.DID)
				require.NoError(t, err)
				require.Equal(t, repo.Version, stored.Version)
				require.True(t, stored.BackfillDone)
				// A tombstone is coverage too. Checking again must not attempt
				// another bio write, including when the record is absent.
				require.NoError(t, db.Exec("CREATE TRIGGER reject_repeat_bio BEFORE INSERT ON bio_pages BEGIN SELECT RAISE(ABORT, 'bio already indexed'); END").Error)
				check()
			})
		}
	}
}

func TestBioUpgradeFallsBackToGetRepo(t *testing.T) {
	dev := devenv.WithDevEnv(t)
	ctx := t.Context()
	atsync, mod := backfillTestSynchronizer(t, dev)
	user := dev.CreateAccount(t)
	createBackfillRecord(t, user, "place.stream.bio.page", "self", &placestream.BioPage{CreatedAt: time.Now().UTC().Format(util.ISO8601)})
	_, err := atsync.SyncBlueskyRepoCached(ctx, user.DID)
	require.NoError(t, err)
	db := mod.(*model.DBModel).DB
	require.NoError(t, db.Where("repo_did = ?", user.DID).Delete(&model.BioPage{}).Error)

	target, err := url.Parse(dev.PDSURL)
	require.NoError(t, err)
	reverse := httputil.NewSingleHostReverseProxy(target)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "com.atproto.sync.getBlocks") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"MethodNotImplemented","message":"not registered"}`))
			return
		}
		reverse.ServeHTTP(w, r)
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	oldClient := SyncHTTPClient
	SyncHTTPClient = &http.Client{Transport: transport, Timeout: oldClient.Timeout}
	defer func() { SyncHTTPClient = oldClient }()

	err = atsync.SyncBioPage(ctx, user.DID)
	require.NoError(t, err)
	page, err := mod.GetBioPage(ctx, user.DID)
	require.NoError(t, err)
	require.NotNil(t, page, "older PDS hosts must still fill bio coverage")
}

func TestBioSyncSkipsTerminalRepo(t *testing.T) {
	atsync, mod, _ := offlineSynchronizer(t)
	const did = "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa"
	require.NoError(t, mod.UpdateRepo(&model.Repo{
		DID: did, Version: "3lq4mksr7i22a", Status: model.RepoStatusDeactivated,
	}))
	require.NoError(t, atsync.SyncBioPage(t.Context(), did), "a terminal repo must not resolve or fetch its missing bio")
	indexed, err := mod.HasIndexedBioPage(t.Context(), did)
	require.NoError(t, err)
	require.False(t, indexed, "skipping a terminal repo does not claim coverage")
}
