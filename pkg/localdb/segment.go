package localdb

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"
	"stream.place/streamplace/pkg/aqtime"
	"stream.place/streamplace/pkg/log"
	"stream.place/streamplace/pkg/placestream"
)

type SegmentMediadataVideo struct {
	TrackID    uint32 `json:"trackId,omitempty"`
	Codec      string `json:"codec,omitempty"`
	SigningKey string `json:"signingKey,omitempty"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	FPSNum     int    `json:"fpsNum"`
	FPSDen     int    `json:"fpsDen"`
	BFrames    bool   `json:"bframes"`
}

type SegmentMediadataAudio struct {
	TrackID    uint32 `json:"trackId,omitempty"`
	Codec      string `json:"codec,omitempty"`
	SigningKey string `json:"signingKey,omitempty"`
	Rate       int    `json:"rate"`
	Channels   int    `json:"channels"`
}

type SegmentMediaData struct {
	Video         []*SegmentMediadataVideo `json:"video"`
	Audio         []*SegmentMediadataAudio `json:"audio"`
	Duration      int64                    `json:"duration"`
	Size          int                      `json:"size"`
	BatchDuration time.Duration            `json:"-"`
}

// Scan scan value into Jsonb, implements sql.Scanner interface
func (j *SegmentMediaData) Scan(value any) error {
	bytes, ok := value.([]byte)
	if !ok {
		return errors.New(fmt.Sprint("Failed to unmarshal JSONB value:", value))
	}

	result := SegmentMediaData{}
	err := json.Unmarshal(bytes, &result)
	*j = SegmentMediaData(result)
	return err
}

// Value return json value, implement driver.Valuer interface
func (j SegmentMediaData) Value() (driver.Value, error) {
	return json.Marshal(j)
}

// ContentRights represents content rights and attribution information
type ContentRights struct {
	CopyrightNotice *string `json:"copyrightNotice,omitempty"`
	CopyrightYear   *int64  `json:"copyrightYear,omitempty"`
	Creator         *string `json:"creator,omitempty"`
	CreditLine      *string `json:"creditLine,omitempty"`
	License         *string `json:"license,omitempty"`
}

// Scan scan value into ContentRights, implements sql.Scanner interface
func (c *ContentRights) Scan(value any) error {
	if value == nil {
		*c = ContentRights{}
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return errors.New(fmt.Sprint("Failed to unmarshal ContentRights value:", value))
	}

	result := ContentRights{}
	err := json.Unmarshal(bytes, &result)
	*c = ContentRights(result)
	return err
}

// Value return json value, implement driver.Valuer interface
func (c ContentRights) Value() (driver.Value, error) {
	return json.Marshal(c)
}

// DistributionPolicy represents distribution policy information
type DistributionPolicy struct {
	DeleteAfterSeconds *int64 `json:"deleteAfterSeconds,omitempty"`
	AllowGenAiTraining *bool  `json:"allowGenAiTraining,omitempty"`
}

// Scan scan value into DistributionPolicy, implements sql.Scanner interface
func (d *DistributionPolicy) Scan(value any) error {
	if value == nil {
		*d = DistributionPolicy{}
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return errors.New(fmt.Sprint("Failed to unmarshal DistributionPolicy value:", value))
	}

	result := DistributionPolicy{}
	err := json.Unmarshal(bytes, &result)
	*d = DistributionPolicy(result)
	return err
}

// Value return json value, implement driver.Valuer interface
func (d DistributionPolicy) Value() (driver.Value, error) {
	return json.Marshal(d)
}

// ContentWarningsSlice is a custom type for storing content warnings as JSON in the database
type ContentWarningsSlice []string

// Scan scan value into ContentWarningsSlice, implements sql.Scanner interface
func (c *ContentWarningsSlice) Scan(value any) error {
	if value == nil {
		*c = ContentWarningsSlice{}
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return errors.New(fmt.Sprint("Failed to unmarshal ContentWarningsSlice value:", value))
	}

	result := ContentWarningsSlice{}
	err := json.Unmarshal(bytes, &result)
	*c = ContentWarningsSlice(result)
	return err
}

// Value return json value, implement driver.Valuer interface
func (c ContentWarningsSlice) Value() (driver.Value, error) {
	return json.Marshal(c)
}

type Segment struct {
	ID                 string               `json:"id"                   gorm:"primaryKey"`
	SigningKeyDID      string               `json:"signingKeyDID"        gorm:"column:signing_key_did"`
	StartTime          time.Time            `json:"startTime"            gorm:"index:latest_segments,priority:2;index:start_time;index:latest_segments_published,priority:2"`
	RepoDID            string               `json:"repoDID"              gorm:"index:latest_segments,priority:1;column:repo_did;index:latest_segments_published,priority:1"`
	Title              string               `json:"title"`
	Size               int                  `json:"size"                gorm:"column:size"`
	MediaData          *SegmentMediaData    `json:"mediaData,omitempty"`
	ContentWarnings    ContentWarningsSlice `json:"contentWarnings,omitempty"`
	ContentRights      *ContentRights       `json:"contentRights,omitempty"`
	DistributionPolicy *DistributionPolicy  `json:"distributionPolicy,omitempty"`
	DeleteAfter        *time.Time           `json:"deleteAfter,omitempty" gorm:"column:delete_after;index:delete_after"`
	Published          bool                 `json:"published"            gorm:"column:published;index:latest_segments_published,priority:3"`
}

func (s *Segment) ToStreamplaceSegment() (*placestream.Segment, error) {
	aqt := aqtime.FromTime(s.StartTime)
	if s.MediaData == nil {
		return nil, fmt.Errorf("media data is nil")
	}
	if len(s.MediaData.Video) == 0 || s.MediaData.Video[0] == nil {
		return nil, fmt.Errorf("video data is nil")
	}
	if len(s.MediaData.Audio) == 0 || s.MediaData.Audio[0] == nil {
		return nil, fmt.Errorf("audio data is nil")
	}
	duration := s.MediaData.Duration
	sizei64 := int64(s.Size)

	// Convert model metadata to streamplace metadata
	var contentRights *placestream.MetadataContentRights
	if s.ContentRights != nil {
		contentRights = &placestream.MetadataContentRights{
			CopyrightNotice: s.ContentRights.CopyrightNotice,
			CopyrightYear:   s.ContentRights.CopyrightYear,
			Creator:         s.ContentRights.Creator,
			CreditLine:      s.ContentRights.CreditLine,
			License:         s.ContentRights.License,
		}
	}

	var contentWarnings *placestream.MetadataContentWarnings
	if len(s.ContentWarnings) > 0 {
		contentWarnings = &placestream.MetadataContentWarnings{
			Warnings: []string(s.ContentWarnings),
		}
	}

	var distributionPolicy *placestream.MetadataDistributionPolicy
	if s.DistributionPolicy != nil && (s.DistributionPolicy.DeleteAfterSeconds != nil || s.DistributionPolicy.AllowGenAiTraining != nil) {
		distributionPolicy = &placestream.MetadataDistributionPolicy{
			DeleteAfter:        s.DistributionPolicy.DeleteAfterSeconds,
			AllowGenAiTraining: s.DistributionPolicy.AllowGenAiTraining,
		}
	}

	videos := make([]placestream.Segment_Video, len(s.MediaData.Video))
	for i, video := range s.MediaData.Video {
		if video == nil {
			return nil, fmt.Errorf("video data is nil")
		}
		codec := video.Codec
		if codec == "" {
			codec = "h264" // metadata persisted before per-track codecs
		}
		videos[i] = placestream.Segment_Video{
			Codec: codec, Width: int64(video.Width), Height: int64(video.Height),
			Framerate: &placestream.Segment_Framerate{Num: int64(video.FPSNum), Den: int64(video.FPSDen)},
			Bframes:   &video.BFrames,
		}
		if video.TrackID != 0 {
			id := strconv.FormatUint(uint64(video.TrackID), 10)
			videos[i].TrackId = &id
		}
		if video.SigningKey != "" {
			videos[i].SigningKey = &video.SigningKey
		}
	}
	audios := make([]placestream.Segment_Audio, len(s.MediaData.Audio))
	for i, audio := range s.MediaData.Audio {
		if audio == nil {
			return nil, fmt.Errorf("audio data is nil")
		}
		codec := audio.Codec
		if codec == "" {
			codec = "opus"
		}
		audios[i] = placestream.Segment_Audio{Codec: codec, Rate: int64(audio.Rate), Channels: int64(audio.Channels)}
		if audio.TrackID != 0 {
			id := strconv.FormatUint(uint64(audio.TrackID), 10)
			audios[i].TrackId = &id
		}
		if audio.SigningKey != "" {
			audios[i].SigningKey = &audio.SigningKey
		}
	}
	return &placestream.Segment{
		LexiconTypeID: "place.stream.segment", Creator: s.RepoDID, Id: s.ID,
		SigningKey: s.SigningKeyDID, StartTime: string(aqt), Duration: &duration, Size: &sizei64,
		ContentRights: contentRights, ContentWarnings: contentWarnings, DistributionPolicy: distributionPolicy,
		Video: videos, Audio: audios,
	}, nil
}

func (m *LocalDatabase) CreateSegment(seg *Segment) error {
	err := m.DB.Model(Segment{}).Create(seg).Error
	if err != nil {
		return err
	}
	return nil
}

// should return the most recent segment for each user, ordered by most recent first
// only includes segments from the last 30 seconds
func (m *LocalDatabase) MostRecentSegments() ([]Segment, error) {
	var segments []Segment
	thirtySecondsAgo := time.Now().Add(-30 * time.Second)

	err := m.DB.Table("segments").
		Select("segments.*").
		Where("start_time > ? AND published = ?", thirtySecondsAgo.UTC(), true).
		Order("start_time DESC").
		Find(&segments).Error
	if err != nil {
		return nil, err
	}
	if segments == nil {
		return []Segment{}, nil
	}

	segmentMap := make(map[string]Segment)
	for _, seg := range segments {
		prev, ok := segmentMap[seg.RepoDID]
		if !ok {
			segmentMap[seg.RepoDID] = seg
		} else {
			if seg.StartTime.After(prev.StartTime) {
				segmentMap[seg.RepoDID] = seg
			}
		}
	}

	filteredSegments := []Segment{}
	for _, seg := range segmentMap {
		filteredSegments = append(filteredSegments, seg)
	}

	return filteredSegments, nil
}

func (m *LocalDatabase) LatestSegmentForUser(user string) (*Segment, error) {
	var seg Segment
	err := m.DB.Model(Segment{}).Where("repo_did = ? AND published = ?", user, true).Order("start_time DESC").First(&seg).Error
	if err != nil {
		return nil, err
	}
	return &seg, nil
}

func (m *LocalDatabase) FilterLiveRepoDIDs(repoDIDs []string) ([]string, error) {
	if len(repoDIDs) == 0 {
		return []string{}, nil
	}

	thirtySecondsAgo := time.Now().Add(-30 * time.Second)

	var liveDIDs []string

	err := m.DB.Table("segments").
		Select("DISTINCT repo_did").
		Where("repo_did IN ? AND start_time > ? AND published = ?", repoDIDs, thirtySecondsAgo.UTC(), true).
		Pluck("repo_did", &liveDIDs).Error

	if err != nil {
		return nil, err
	}

	return liveDIDs, nil
}

func (m *LocalDatabase) LatestSegmentsForUser(user string, limit int, includeUnpublished bool, before *time.Time, after *time.Time) ([]Segment, error) {
	var segs []Segment
	if before == nil {
		later := time.Now().Add(1000 * time.Hour)
		before = &later
	}
	if after == nil {
		earlier := time.Time{}
		after = &earlier
	}
	query := m.DB.Model(Segment{}).Where("repo_did = ? AND start_time < ? AND start_time > ?", user, before.UTC(), after.UTC())
	if !includeUnpublished {
		query = query.Where("published = ?", true)
	}
	err := query.Order("start_time DESC").Limit(limit).Find(&segs).Error
	if err != nil {
		return nil, err
	}
	return segs, nil
}

func (m *LocalDatabase) GetSegment(id string) (*Segment, error) {
	var seg Segment

	err := m.DB.Model(&Segment{}).
		Preload("Repo").
		Where("id = ?", id).
		First(&seg).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return &seg, nil
}

func (m *LocalDatabase) GetExpiredSegments(ctx context.Context) ([]Segment, error) {

	var expiredSegments []Segment
	now := time.Now()
	err := m.DB.
		Where("delete_after IS NOT NULL AND delete_after < ?", now.UTC()).
		Find(&expiredSegments).Error
	if err != nil {
		return nil, err
	}

	return expiredSegments, nil
}

func (m *LocalDatabase) DeleteSegment(ctx context.Context, id string) error {
	return m.DB.Delete(&Segment{}, "id = ?", id).Error
}

func (m *LocalDatabase) StartSegmentCleaner(ctx context.Context) error {
	err := m.SegmentCleaner(ctx)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			err := m.SegmentCleaner(ctx)
			if err != nil {
				log.Error(ctx, "Failed to clean segments", "error", err)
			}
		}
	}
}

func (m *LocalDatabase) SegmentCleaner(ctx context.Context) error {
	// Calculate the cutoff time (10 minutes ago)
	cutoffTime := aqtime.FromTime(time.Now().Add(-10 * time.Minute)).Time()

	// Find all unique repo_did values
	var repoDIDs []string
	if err := m.DB.Model(&Segment{}).Distinct("repo_did").Pluck("repo_did", &repoDIDs).Error; err != nil {
		log.Error(ctx, "Failed to get unique repo_dids for segment cleaning", "error", err)
		return err
	}

	// For each user, keep their last 10 segments and delete older ones
	for _, repoDID := range repoDIDs {
		// Get IDs of the last 10 segments for this user
		var keepSegmentIDs []string
		if err := m.DB.Model(&Segment{}).
			Where("repo_did = ?", repoDID).
			Order("start_time DESC").
			Limit(10).
			Pluck("id", &keepSegmentIDs).Error; err != nil {
			log.Error(ctx, "Failed to get segment IDs to keep", "repo_did", repoDID, "error", err)
			return err
		}

		// Delete old segments except the ones we want to keep
		result := m.DB.Where("repo_did = ? AND start_time < ? AND id NOT IN ?",
			repoDID, cutoffTime, keepSegmentIDs).Delete(&Segment{})

		if result.Error != nil {
			log.Error(ctx, "Failed to clean old segments", "repo_did", repoDID, "error", result.Error)
		} else if result.RowsAffected > 0 {
			log.Log(ctx, "Cleaned old segments", "repo_did", repoDID, "count", result.RowsAffected)
		}
	}
	return nil
}
