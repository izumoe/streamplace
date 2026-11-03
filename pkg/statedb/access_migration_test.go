package statedb_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/streamplace/oatproxy/pkg/oatproxy"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/accessctl"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/statedb"
)

func TestAccessMigrationPreservesExistingNodeState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	legacyDB, err := legacy.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, legacyDB.Close()) })
	// These models and the oatproxy version are unchanged from the pre-access
	// schema. Create their tables without running the new node migrations.
	require.NoError(t, legacy.AutoMigrate(&statedb.Config{}, &oatproxy.OAuthSession{}))
	now := time.Date(2026, time.November, 1, 12, 0, 0, 0, time.UTC)
	wantConfig := statedb.Config{Key: "existing-node-setting", Value: []byte{0, 1, 2, 255}, CreatedAt: now, UpdatedAt: now}
	wantSession := oatproxy.OAuthSession{
		DID: "did:plc:existing-account", DownstreamDPoPJKT: "existing-session",
		UpstreamAccessToken: "persisted-upstream-token", DownstreamAccessToken: "persisted-downstream-token",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, legacy.Create(&wantConfig).Error)
	require.NoError(t, legacy.Create(&wantSession).Error)
	for _, table := range []string{"access_entries", "access_revisions", "access_publisher_epochs"} {
		require.False(t, legacy.Migrator().HasTable(table))
	}
	require.NoError(t, legacyDB.Close())

	cli := &config.CLI{DBURL: "sqlite://" + path, BroadcasterHost: "node.test"}
	state, err := statedb.MakeDB(t.Context(), cli, nil, nil)
	require.NoError(t, err)
	db, err := state.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	gotConfig, err := state.GetConfig(wantConfig.Key)
	require.NoError(t, err)
	require.Equal(t, &wantConfig, gotConfig)
	gotSessions, err := state.ListOAuthSessions()
	require.NoError(t, err)
	require.Equal(t, []oatproxy.OAuthSession{wantSession}, gotSessions)
	revision, entries, generations, err := state.ReadAccess(t.Context(), cli.ServerDID())
	require.NoError(t, err, "upgrade installs all access tables without requiring a fresh database")
	require.Zero(t, revision)
	require.Empty(t, entries)
	require.Empty(t, generations)
	bootstrap, err := cli.AccessBootstrap()
	require.NoError(t, err)
	controller, err := accessctl.New(t.Context(), bootstrap, state, nil)
	require.NoError(t, err)
	require.False(t, controller.Check(wantSession.DID, access.Streamer).Allowed, "an existing sign-in does not approve publishing")
	require.True(t, controller.Check("", access.Viewer).Allowed)
	require.True(t, controller.Check(wantSession.DID, access.VOD).Allowed, "an empty legacy stream allowlist preserves VOD eligibility")
	denial, err := controller.Create(t.Context(), wantSession.DID, access.Streamer, access.Deny, "restricted after upgrade", nil)
	require.NoError(t, err)
	require.NoError(t, controller.Delete(t.Context(), denial.ID))
	revision, entries, generations, err = state.ReadAccess(t.Context(), cli.ServerDID())
	require.NoError(t, err)
	require.NotZero(t, revision)
	require.Empty(t, entries)
	require.NotZero(t, generations[wantSession.DID])
}
