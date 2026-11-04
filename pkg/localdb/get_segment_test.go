package localdb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
)

func TestGetSegmentReturnsStoredAdmission(t *testing.T) {
	previousLogger := config.GormLogger
	config.DisableSQLLogging()
	t.Cleanup(func() { config.GormLogger = previousLogger })
	database, err := MakeDB(":memory:")
	require.NoError(t, err)
	sqlDB, err := database.(*LocalDatabase).DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	want := &Segment{
		ID: "urn:c2pa:source", RepoDID: "did:plc:streamer", Published: true,
		StartTime: time.Unix(1700000000, 0).UTC(),
		MediaData: &SegmentMediaData{Audio: []*SegmentMediadataAudio{{TrackID: 2, Codec: "opus", Rate: 48000, Channels: 2}}},
	}
	require.NoError(t, database.CreateSegment(want))
	got, err := database.GetSegment(want.ID)
	require.NoError(t, err, "a stored segment has no external repository relation to preload")
	require.Equal(t, want, got)
	missing, err := database.GetSegment("unknown-source")
	require.NoError(t, err)
	require.Nil(t, missing)
}
