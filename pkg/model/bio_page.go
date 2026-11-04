package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bluesky-social/indigo/atproto/atdata"
	"github.com/bluesky-social/indigo/atproto/syntax"
	glex "github.com/streamplace/glex/runtime"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"stream.place/streamplace/pkg/placestream"
)

// BioPage retains the latest repository revision even when the record is deleted.
// A nil Record is a tombstone, preventing replay from resurrecting an older bio.
type BioPage struct {
	RepoDID string `gorm:"primaryKey;column:repo_did"`
	RepoRev string
	Record  *[]byte
}

func (m *BioPage) ToStreamplaceBioPage() (*placestream.BioPage, error) {
	if m == nil || m.Record == nil {
		return nil, fmt.Errorf("bio page is nil")
	}
	// Decode AT Protocol values before JSON so unknown unions retain their
	// bytes and links in the same wire format used by PDS getRecord responses.
	data, err := atdata.UnmarshalCBOR(*m.Record)
	if err != nil {
		return nil, fmt.Errorf("decoding bio page: %w", err)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encoding bio page JSON: %w", err)
	}
	return glex.JsonDecodeAs[placestream.BioPage](encoded)
}

func (m *DBModel) CreateBioPage(ctx context.Context, page *BioPage) error {
	if _, err := syntax.ParseTID(page.RepoRev); err != nil {
		return fmt.Errorf("invalid bio repository revision: %w", err)
	}
	return m.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "repo_did"}},
		DoUpdates: clause.AssignmentColumns([]string{"repo_rev", "record"}),
		Where:     clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "excluded.repo_rev > COALESCE(bio_pages.repo_rev, '')"}}},
	}).Create(page).Error
}

func (m *DBModel) DeleteBioPage(ctx context.Context, repoDID, repoRev string) error {
	return m.CreateBioPage(ctx, &BioPage{RepoDID: repoDID, RepoRev: repoRev})
}

func (m *DBModel) HasIndexedBioPage(ctx context.Context, repoDID string) (bool, error) {
	// Legacy rows without a revision have not been reconciled with the repo.
	var count int64
	err := m.DB.WithContext(ctx).Model(&BioPage{}).Where("repo_did = ? AND repo_rev <> ''", repoDID).Count(&count).Error
	return count != 0, err
}

func (m *DBModel) GetBioPage(ctx context.Context, repoDID string) (*BioPage, error) {
	var bioPage BioPage
	err := m.DB.WithContext(ctx).Where("repo_did = ? AND record IS NOT NULL", repoDID).First(&bioPage).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &bioPage, nil
}
