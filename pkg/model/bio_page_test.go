package model

import (
	"bytes"
	"context"
	"fmt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/placestream"
)

func TestBioPageRevisionOrdersUpdatesAndDeletes(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	m, err := MakeDB(dir)
	require.NoError(t, err)
	db, err := m.(*DBModel).DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	const did = "did:plc:bio-owner"
	save := func(rev, text string) {
		t.Helper()
		var encoded bytes.Buffer
		require.NoError(t, (&placestream.BioPage{CreatedAt: "2026-11-03T00:00:00Z", Description: &placestream.BioPage_Description{Plaintext: text}}).MarshalCBOR(&encoded))
		raw := encoded.Bytes()
		require.NoError(t, m.CreateBioPage(ctx, &BioPage{RepoDID: did, RepoRev: rev, Record: &raw}))
	}
	check := func(mod Model, text string) {
		t.Helper()
		row, err := mod.GetBioPage(ctx, did)
		require.NoError(t, err)
		if text == "" {
			require.Nil(t, row)
			return
		}
		require.NotNil(t, row)
		page, err := row.ToStreamplaceBioPage()
		require.NoError(t, err)
		require.Equal(t, text, page.Description.Plaintext)
	}
	save("3lq4mksr7i22a", "first")
	save("3lq4mksr7i22c", "newer")
	save("3lq4mksr7i22b", "delayed old update")
	check(m, "newer")
	require.NoError(t, m.DeleteBioPage(ctx, did, "3lq4mksr7i22d"))
	check(m, "")
	save("3lq4mksr7i22c", "delayed create after deletion")
	check(m, "")
	require.NoError(t, db.Close())
	reloaded, err := MakeDB(dir)
	require.NoError(t, err)
	reloadDB, err := reloaded.(*DBModel).DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reloadDB.Close()) })
	check(reloaded, "")
	m = reloaded
	save("3lq4mksr7i22b", "old create after restart")
	check(m, "")
	save("3lq4mksr7i22e", "recreated")
	require.NoError(t, m.DeleteBioPage(ctx, did, "3lq4mksr7i22d"))
	check(m, "recreated")
}

func TestBioPageRequiresRevision(t *testing.T) {
	m, err := MakeDB(":memory:")
	require.NoError(t, err)
	ctx := context.Background()
	require.Error(t, m.CreateBioPage(ctx, &BioPage{RepoDID: "did:plc:owner"}))
	require.Error(t, m.DeleteBioPage(ctx, "did:plc:owner", ""))
}

func TestBioPageRecord(t *testing.T) {
	page := placestream.BioPage{
		CreatedAt:   "2026-10-02T00:00:00Z",
		Description: &placestream.BioPage_Description{Plaintext: "hello"},
		Socials:     []placestream.BioDefs_Social{{Platform: "website", Url: "https://example.com"}},
		Layout: &placestream.BioPage_Layout{BioLayoutsPanels: &placestream.BioLayoutsPanels{
			Panels: []placestream.BioLayoutsPanels_Panel{{
				Blocks: []placestream.BioLayoutsPanels_BlockEntry{{
					Block: placestream.BioLayoutsPanels_BlockEntry_Block{
						BioBlocksText: &placestream.BioBlocksText{Plaintext: "about me"},
					},
				}},
			}},
		}},
	}
	var buf bytes.Buffer
	require.NoError(t, page.MarshalCBOR(&buf))
	record := buf.Bytes()
	decoded, err := (&BioPage{Record: &record}).ToStreamplaceBioPage()
	require.NoError(t, err)
	require.Equal(t, page.CreatedAt, decoded.CreatedAt)
	require.Equal(t, "hello", decoded.Description.Plaintext)
	require.Equal(t, "https://example.com", decoded.Socials[0].Url)
	require.Equal(t, "about me", decoded.Layout.BioLayoutsPanels.Panels[0].Blocks[0].Block.BioBlocksText.Plaintext)

	for name, row := range map[string]*BioPage{
		"nil page":     nil,
		"nil record":   {},
		"invalid CBOR": {Record: &[]byte{0xff}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := row.ToStreamplaceBioPage()
			require.Error(t, err)
		})
	}
	buf.Reset()
	require.NoError(t, (&placestream.Livestream{CreatedAt: "2026-11-03T00:00:00Z"}).MarshalCBOR(&buf))
	record = buf.Bytes()
	_, err = (&BioPage{Record: &record}).ToStreamplaceBioPage()
	require.Error(t, err, "a different record type must not be served as a bio")
}

func TestBioPageMigratesUnversionedRows(t *testing.T) {
	dir := t.TempDir()
	var encoded bytes.Buffer
	require.NoError(t, (&placestream.BioPage{CreatedAt: "2026-11-03T00:00:00Z"}).MarshalCBOR(&encoded))
	raw := encoded.Bytes()

	legacy, err := gorm.Open(sqlite.Open(filepath.Join(dir, fmt.Sprintf("index_%d.sqlite", DBRevision))), &gorm.Config{})
	require.NoError(t, err)
	db, err := legacy.DB()
	require.NoError(t, err)
	require.NoError(t, legacy.Exec("CREATE TABLE bio_pages (repo_did text PRIMARY KEY, record blob)").Error)
	require.NoError(t, legacy.Exec("INSERT INTO bio_pages (repo_did, record) VALUES (?, ?)", "did:plc:owner", raw).Error)
	require.NoError(t, db.Close())
	mod, err := MakeDB(dir)
	require.NoError(t, err)
	sqlDB, err := mod.(*DBModel).DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	indexed, err := mod.HasIndexedBioPage(t.Context(), "did:plc:owner")
	require.NoError(t, err)
	require.False(t, indexed, "a legacy bio without a revision has not been reconciled")
	require.NoError(t, mod.CreateBioPage(t.Context(), &BioPage{RepoDID: "did:plc:owner", RepoRev: "3lq4mksr7i22a", Record: &raw}))
	row, err := mod.GetBioPage(t.Context(), "did:plc:owner")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "3lq4mksr7i22a", row.RepoRev, "a legacy row with a newly added nullable revision must accept current records")
	require.Equal(t, raw, *row.Record)
	indexed, err = mod.HasIndexedBioPage(t.Context(), "did:plc:owner")
	require.NoError(t, err)
	require.True(t, indexed)
}
