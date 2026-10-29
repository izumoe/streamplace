package localdb

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
)

func TestSegmentPreservesAllTracks(t *testing.T) {
	var metadata SegmentMediaData
	require.NoError(t, json.Unmarshal([]byte(`{"video":[{"trackId":7,"codec":"h264","width":1920,"height":1080},{"trackId":20,"codec":"av1","width":1280,"height":720}],"audio":[{"trackId":42,"codec":"aac","rate":48000,"channels":2},{"trackId":99,"codec":"opus","rate":48000,"channels":2}]}`), &metadata))
	segment, err := (&Segment{MediaData: &metadata}).ToStreamplaceSegment()
	require.NoError(t, err)
	require.Len(t, segment.Video, 2)
	require.Len(t, segment.Audio, 2)
	encoded, err := json.Marshal(segment)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(encoded, &got))
	video := got["video"].([]any)
	require.Equal(t, "h264", video[0].(map[string]any)["codec"])
	require.Equal(t, "7", video[0].(map[string]any)["trackId"])
	require.Equal(t, "av1", video[1].(map[string]any)["codec"])
	audio := got["audio"].([]any)
	require.Equal(t, "aac", audio[0].(map[string]any)["codec"])
	require.Equal(t, "42", audio[0].(map[string]any)["trackId"])
	require.Equal(t, "opus", audio[1].(map[string]any)["codec"])
}

func TestSegmentLegacyMetadata(t *testing.T) {
	segment, err := (&Segment{MediaData: &SegmentMediaData{
		Video: []*SegmentMediadataVideo{{Width: 640, Height: 360}},
		Audio: []*SegmentMediadataAudio{{Rate: 48000, Channels: 2}},
	}}).ToStreamplaceSegment()
	require.NoError(t, err)
	require.Equal(t, "h264", segment.Video[0].Codec)
	require.Equal(t, "opus", segment.Audio[0].Codec)
}

func TestSegmentPerf(t *testing.T) {
	config.DisableSQLLogging()
	// dburl := filepath.Join(t.TempDir(), "test.db")
	db, err := MakeDB(":memory:")
	require.NoError(t, err)
	// Create a ldb instance
	ldb := db.(*LocalDatabase)
	t.Cleanup(func() {
		// os.Remove(dburl)
	})

	defer config.EnableSQLLogging()
	// Create 250000 segments with timestamps 1 hour ago, each one second apart
	wg := sync.WaitGroup{}
	segCount := 250000
	wg.Add(segCount)
	baseTime := time.Now()
	for i := 0; i < segCount; i++ {
		segment := &Segment{
			ID:        fmt.Sprintf("segment-%d", i),
			RepoDID:   "did:plc:test123",
			StartTime: baseTime.Add(-time.Duration(i) * time.Second).UTC(),
		}
		go func() {
			defer wg.Done()
			err = ldb.DB.Create(segment).Error
			require.NoError(t, err)
		}()
	}
	wg.Wait()

	startTime := time.Now()
	wg = sync.WaitGroup{}
	runs := 1000
	wg.Add(runs)
	for i := 0; i < runs; i++ {
		go func() {
			defer wg.Done()
			_, err := ldb.MostRecentSegments()
			require.NoError(t, err)
			// require.Len(t, segments, 1)
		}()
	}
	wg.Wait()
	fmt.Printf("Time taken: %s\n", time.Since(startTime))
	require.Less(t, time.Since(startTime), 10*time.Second)
}
