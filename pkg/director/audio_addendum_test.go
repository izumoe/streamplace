package director

import (
	"bytes"
	"context"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/accessctl"
	"stream.place/streamplace/pkg/aqtime"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/gstinit"
	"stream.place/streamplace/pkg/localdb"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/muxl"
	"stream.place/streamplace/pkg/placestream"
	"stream.place/streamplace/pkg/statedb"
)

// The second source's signed audio is a real, independently playable continuation
// of the first source's clock. Signature admission is covered in media; this
// test covers the director's ordered handoff to actual playback consumers.
func audioDeliveryFixture(t *testing.T) (source, audio []byte) {
	t.Helper()
	gstinit.InitGST()
	input, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "h264-opus-frag.mp4"))
	require.NoError(t, err)
	signer, err := media.NewEphemeralMediaSigner("audio-delivery")
	require.NoError(t, err)
	signer.PrebuiltManifest = []byte(`{
  "title":"audio delivery","assertions":[
   {"label":"c2pa.actions","data":{"actions":[{"action":"c2pa.created"},{"action":"c2pa.published"}]}},
   {"label":"cawg.metadata","data":{"@context":{"dc":"http://purl.org/dc/elements/1.1/"},"dc:creator":"did:example:audio-delivery","dc:title":"audio delivery","dc:date":"1970-01-01T00:00:00.000Z"}}
  ]}`)
	events := make(chan *muxl.MuxlEvent, 16)
	done := make(chan error, 1)
	go func() {
		done <- signer.SignSegmentStream(t.Context(), bytes.NewReader(input), events)
		close(events)
	}()
	var segments [][]byte
	for event := range events {
		if event.Type != "signed-segment" {
			continue
		}
		ids := make([]string, 0, len(event.Tracks))
		for id := range event.Tracks {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		var segment []byte
		for _, id := range ids {
			segment = append(segment, event.Tracks[id]...)
		}
		segments = append(segments, segment)
	}
	require.NoError(t, <-done)
	require.GreaterOrEqual(t, len(segments), 2)
	verified, err := media.ValidateMP4MediaC2PA(t.Context(), segments[0])
	require.NoError(t, err)
	require.True(t, verified.Meta.Published)
	require.Equal(t, "did:example:audio-delivery", verified.Meta.Creator)
	tracks, err := media.MuxlTracks(t.Context(), segments[1])
	require.NoError(t, err)
	require.NotEmpty(t, tracks["audio2"])
	return segments[0], tracks["audio2"]
}

func TestAudioAddendumWaitsForSourcePublication(t *testing.T) {
	previousSQLLogger := config.GormLogger
	config.DisableSQLLogging()
	t.Cleanup(func() { config.GormLogger = previousSQLLogger })
	source, audio := audioDeliveryFixture(t)
	verified, err := media.ValidateMP4MediaC2PA(t.Context(), source)
	require.NoError(t, err)
	require.NotNil(t, verified.Manifest.Label)
	sourceID := *verified.Manifest.Label
	for _, test := range []struct {
		name                     string
		sourcePublic, tailPublic bool
		videoPending             bool
	}{
		{"private", false, false, false}, {"public", true, true, false}, {"source refused publication", false, true, false}, {"tail restricts publication", true, false, false}, {"pending video rendition", true, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			published := test.sourcePublic && test.tailPublic
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cli := &config.CLI{BroadcasterHost: "node.test", DataDir: t.TempDir(), AllowedStreams: []string{"did:example:audio-delivery"}}
			b := bus.NewBus()
			mm, err := media.MakeMediaManager(ctx, cli, nil, nil, b, nil, nil)
			require.NoError(t, err)
			started := make(chan struct{})
			close(started)
			localDB, err := localdb.MakeDB(":memory:")
			require.NoError(t, err)
			db, err := localDB.(*localdb.LocalDatabase).DB.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			ss := &StreamSession{mm: mm, cli: cli, bus: b, ctx: ctx, started: started, g: &errgroup.Group{}, sourceLane: newLane(), renditions: newLane(), localDB: localDB}
			const did = "did:example:audio-delivery"
			canonical := b.SubscribeSegment(ctx, did, "source")
			defer b.UnsubscribeSegment(ctx, did, "source", canonical)
			playback := b.SubscribeSegment(ctx, did, media.WebRTCSourceRendition)
			defer b.UnsubscribeSegment(ctx, did, media.WebRTCSourceRendition, playback)
			addenda := b.SubscribeSegment(ctx, did, media.RenditionsChannel)
			defer b.UnsubscribeSegment(ctx, did, media.RenditionsChannel, addenda)
			start := time.Now()
			require.NoError(t, mm.FeedLiveRenditions(ctx, did, source, start, test.sourcePublic))
			require.NoError(t, localDB.CreateSegment(&localdb.Segment{ID: sourceID, RepoDID: did, StartTime: start, Published: test.sourcePublic}))
			first := ss.sourceLane.turn()
			defer first.release()
			var videoTurn *turn
			if test.videoPending {
				cli.TranscoderURL = "http://127.0.0.1:1"
				videoTurn = ss.renditions.turn()
				defer videoTurn.release()
			}
			require.NoError(t, ss.NewSegment(ctx, &media.NewSegmentNotification{
				Segment: &localdb.Segment{ID: sourceID, RepoDID: did, StartTime: start},
				Muxl:    audio, AudioOnly: true, Local: true,
				Metadata: &media.SegmentMetadata{Creator: did, StartTime: aqtime.FromTime(start), Published: test.tailPublic},
			}))
			require.Eventually(t, func() bool {
				ss.sourceLane.mu.Lock()
				defer ss.sourceLane.mu.Unlock()
				return ss.sourceLane.waiters[1] != nil
			}, time.Second, time.Millisecond)
			require.Empty(t, addenda.C, "audio cannot overtake the source's packetizer")
			require.Empty(t, playback.C)
			require.NoError(t, ss.AddToWebRTC(ctx, &placestream.Segment{
				Creator: did, Video: []placestream.Segment_Video{{Codec: "h264"}},
			}, "source", &bus.Seg{Filepath: sourceID, Muxl: source, Published: test.sourcePublic}, first))
			if test.videoPending {
				require.Eventually(t, func() bool {
					ss.renditions.mu.Lock()
					defer ss.renditions.mu.Unlock()
					return ss.renditions.waiters[1] != nil
				}, time.Second, time.Millisecond)
				require.Len(t, playback.C, 1, "the audio tail must wait for the final compatible video rendition")
				videoTurn.release()
			}
			require.NoError(t, ss.g.Wait())
			require.Len(t, canonical.C, 2, "canonical source and audio continuation preserve publication order")
			require.Equal(t, source, (<-canonical.C).Muxl)
			require.Empty(t, addenda.C, "audio shares the ordered source channel rather than racing video renditions")
			addendum := <-canonical.C
			require.Equal(t, audio, addendum.Muxl)
			require.True(t, addendum.AudioOnly)
			require.Equal(t, published, addendum.Published)
			require.Len(t, playback.C, 2)
			video, tail := <-playback.C, <-playback.C
			require.NotEmpty(t, video.PacketizedData.Video)
			require.Empty(t, tail.PacketizedData.Video)
			require.NotEmpty(t, tail.PacketizedData.Audio)
			require.Equal(t, published, tail.Published)
			_, visible := mm.GetLiveWindow(did)
			require.Equal(t, published, visible, "HLS retains the source's visibility")
		})
	}
}

func TestAudioAddendumCannotPublishWithoutAdmittedSource(t *testing.T) {
	previousSQLLogger := config.GormLogger
	config.DisableSQLLogging()
	t.Cleanup(func() { config.GormLogger = previousSQLLogger })
	_, audio := audioDeliveryFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cli := &config.CLI{BroadcasterHost: "node.test", DataDir: t.TempDir()}
	b := bus.NewBus()
	mm, err := media.MakeMediaManager(ctx, cli, nil, nil, b, nil, nil)
	require.NoError(t, err)
	started := make(chan struct{})
	close(started)
	ss := &StreamSession{mm: mm, cli: cli, bus: b, ctx: ctx, started: started, g: &errgroup.Group{}, sourceLane: newLane()}
	const did = "did:example:audio-delivery"
	sub := b.SubscribeSegment(ctx, did, media.RenditionsChannel)
	defer b.UnsubscribeSegment(ctx, did, media.RenditionsChannel, sub)
	localDB, err := localdb.MakeDB(":memory:")
	require.NoError(t, err)
	db, err := localDB.(*localdb.LocalDatabase).DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ss.localDB = localDB
	require.ErrorContains(t, ss.NewSegment(ctx, &media.NewSegmentNotification{
		Segment: &localdb.Segment{ID: "rejected-source", RepoDID: did, StartTime: time.Now()},
		Muxl:    audio, AudioOnly: true, Metadata: &media.SegmentMetadata{Creator: did, Published: true},
	}), "source was not admitted")
	require.NoError(t, ss.g.Wait())
	require.Empty(t, sub.C, "a bitrate-rejected or abandoned source cannot release its audio")
	_, published := mm.GetLiveWindow(did)
	require.False(t, published)
}

func TestAudioAddendumCannotOutlivePublisherAdmission(t *testing.T) {
	previousSQLLogger := config.GormLogger
	config.DisableSQLLogging()
	t.Cleanup(func() { config.GormLogger = previousSQLLogger })
	_, audio := audioDeliveryFixture(t)
	ctx := t.Context()
	const did = "did:plc:queued-audio"
	cli := &config.CLI{BroadcasterHost: "node.test", DBURL: ":memory:"}
	state, err := statedb.MakeDB(ctx, cli, nil, nil)
	require.NoError(t, err)
	db, err := state.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	b := bus.NewBus()
	controller, err := accessctl.New(ctx, access.Bootstrap{
		Authority: "did:web:node.test",
		Entries:   []access.Entry{{Subject: did, Capability: access.Streamer, Effect: access.Allow}},
	}, state, b)
	require.NoError(t, err)
	mm := media.NewOffline(cli)
	mm.Access = controller
	localDB, err := localdb.MakeDB(":memory:")
	require.NoError(t, err)
	localSQL, err := localDB.(*localdb.LocalDatabase).DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, localSQL.Close()) })
	require.NoError(t, localDB.CreateSegment(&localdb.Segment{ID: "admitted-source", RepoDID: did, Published: true}))
	started := make(chan struct{})
	close(started)
	ss := &StreamSession{mm: mm, cli: cli, bus: b, ctx: ctx, started: started, g: &errgroup.Group{}, sourceLane: newLane(), localDB: localDB}
	first := ss.sourceLane.turn()
	defer first.release()
	generation := controller.Check(did, access.Streamer).Generation
	notification := &media.NewSegmentNotification{
		Segment: &localdb.Segment{ID: "admitted-source", RepoDID: did},
		Muxl:    audio, AudioOnly: true, Local: true, PublisherGeneration: generation,
		Metadata: &media.SegmentMetadata{Creator: did, Published: true},
	}
	var logs bytes.Buffer
	logger := slog.Default()
	previousVerbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "1"))
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() {
		slog.SetDefault(logger)
		require.NoError(t, flag.Set("v", previousVerbosity))
	})
	require.NoError(t, ss.NewSegment(ctx, notification))
	require.Eventually(t, func() bool {
		ss.sourceLane.mu.Lock()
		defer ss.sourceLane.mu.Unlock()
		return ss.sourceLane.waiters[1] != nil
	}, time.Second, time.Millisecond)
	denial, err := controller.Create(ctx, did, access.Streamer, access.Deny, "revoked", nil)
	require.NoError(t, err)
	require.NoError(t, controller.Delete(ctx, denial.ID))
	first.release()
	require.NoError(t, ss.g.Wait())
	require.Contains(t, logs.String(), "must reconnect")
	require.False(t, b.HasSegments(did, media.RenditionsChannel))
	window, _ := mm.GetLiveWindow(did)
	require.Nil(t, window, "reapproval cannot release a revoked session's queued audio")
	require.ErrorContains(t, ss.NewSegment(ctx, notification), "must reconnect", "a stale notification also fails before being queued")
}
