package statedb

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"stream.place/streamplace/pkg/access"
)

// AccessEntry is private node policy. The two effects coexist so removing a ban
// does not remove an account's separate allowance.
type AccessEntry struct {
	ID         string            `gorm:"primaryKey"`
	Authority  string            `gorm:"uniqueIndex:access_subject_effect"`
	Subject    string            `gorm:"uniqueIndex:access_subject_effect"`
	Capability access.Capability `gorm:"uniqueIndex:access_subject_effect"`
	Effect     access.Effect     `gorm:"uniqueIndex:access_subject_effect"`
	Reason     string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
}

type AccessRevision struct {
	Authority string `gorm:"primaryKey"`
	Revision  uint64
}

// AccessPublisherEpoch outlives entries, including bans that have been removed.
type AccessPublisherEpoch struct {
	Authority  string `gorm:"primaryKey"`
	Subject    string `gorm:"primaryKey"`
	Generation uint64
}

func (AccessPublisherEpoch) TableName() string { return "access_publisher_epochs" }

func (entry AccessEntry) view() access.Entry {
	return access.Entry{ID: entry.ID, Authority: entry.Authority, Subject: entry.Subject, Capability: entry.Capability, Effect: entry.Effect, Reason: entry.Reason, CreatedAt: entry.CreatedAt, ExpiresAt: entry.ExpiresAt, Source: access.Stored}
}

func (state *StatefulDB) ReadAccess(ctx context.Context, authority string) (uint64, []access.Entry, map[string]uint64, error) {
	var revision AccessRevision
	var rows []AccessEntry
	var epochs []AccessPublisherEpoch
	err := state.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("authority = ?", authority).Limit(1).Find(&revision).Error; err != nil {
			return err
		}
		if err := tx.Where("authority = ?", authority).Order("created_at, id").Find(&rows).Error; err != nil {
			return err
		}
		return tx.Where("authority = ?", authority).Find(&epochs).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	entries := make([]access.Entry, len(rows))
	for i, row := range rows {
		entries[i] = row.view()
	}
	generations := make(map[string]uint64, len(epochs))
	for _, epoch := range epochs {
		generations[epoch.Subject] = epoch.Generation
	}
	return revision.Revision, entries, generations, err
}

// Lock the revision row before inspecting entries. This serializes policy writes
// across processes in both SQLite and PostgreSQL, not just within one controller.
func lockAccess(tx *gorm.DB, authority string) error {
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&AccessRevision{Authority: authority}).Error; err != nil {
		return err
	}
	return tx.Model(&AccessRevision{}).Where("authority = ?", authority).UpdateColumn("revision", gorm.Expr("revision + 0")).Error
}

func advanceAccess(tx *gorm.DB, entry AccessEntry, invalidate bool) error {
	if invalidate {
		epoch := AccessPublisherEpoch{Authority: entry.Authority, Subject: entry.Subject, Generation: 1}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "authority"}, {Name: "subject"}}, DoUpdates: clause.Assignments(map[string]any{"generation": gorm.Expr("access_publisher_epochs.generation + 1")})}).Create(&epoch).Error; err != nil {
			return err
		}
	}
	return tx.Model(&AccessRevision{}).Where("authority = ?", entry.Authority).UpdateColumn("revision", gorm.Expr("revision + 1")).Error
}

func (state *StatefulDB) CreateAccessEntry(ctx context.Context, input access.Entry) (access.Entry, error) {
	if err := access.ValidateMutation(input); err != nil {
		return access.Entry{}, err
	}
	entry := AccessEntry{ID: uuid.NewString(), Authority: input.Authority, Subject: input.Subject, Capability: input.Capability, Effect: input.Effect, Reason: input.Reason, CreatedAt: input.CreatedAt, ExpiresAt: input.ExpiresAt}
	// PostgreSQL stores microsecond timestamps. Normalize before comparisons so
	// identical retries behave the same in both supported databases.
	entry.CreatedAt = entry.CreatedAt.UTC().Truncate(time.Microsecond)
	if entry.ExpiresAt != nil {
		expiry := entry.ExpiresAt.UTC().Truncate(time.Microsecond)
		entry.ExpiresAt = &expiry
	}

	err := state.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockAccess(tx, entry.Authority); err != nil {
			return err
		}
		var existing AccessEntry
		if err := tx.Where("authority = ? AND subject = ? AND capability = ? AND effect = ?", entry.Authority, entry.Subject, entry.Capability, entry.Effect).Limit(1).Find(&existing).Error; err != nil {
			return err
		}
		if existing.ID != "" {
			sameExpiry := existing.ExpiresAt == nil && entry.ExpiresAt == nil || existing.ExpiresAt != nil && entry.ExpiresAt != nil && existing.ExpiresAt.Equal(*entry.ExpiresAt)
			if existing.Reason != entry.Reason || !sameExpiry {
				return access.ErrConflict
			}
			entry = existing
			return nil
		}
		if err := tx.Create(&entry).Error; err != nil {
			return err
		}
		// Replacing an expired allowance requires deleting its exact entry first;
		// that deletion advances the generation before a new allowance can exist.
		return advanceAccess(tx, entry, entry.Capability == access.Streamer && entry.Effect == access.Deny)
	})
	return entry.view(), err
}

func (state *StatefulDB) DeleteAccessEntry(ctx context.Context, authority, id string) error {
	return state.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockAccess(tx, authority); err != nil {
			return err
		}
		var entry AccessEntry
		if err := tx.Where("authority = ? AND id = ?", authority, id).Limit(1).Find(&entry).Error; err != nil {
			return err
		}
		if entry.ID == "" {
			return access.ErrNotFound
		}
		if err := tx.Delete(&entry).Error; err != nil {
			return err
		}
		return advanceAccess(tx, entry, entry.Capability == access.Streamer && entry.Effect == access.Allow)
	})
}
