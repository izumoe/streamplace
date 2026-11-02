package media

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/accessctl"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/statedb"
)

func TestPublisherDenialPrecedesIngestSetup(t *testing.T) {
	signer := newBareSegmentSigner(t)
	signer.StreamerName = signer.DID()
	policy, err := access.NewSnapshot(access.Bootstrap{Authority: "did:web:node.test"}, 1, nil)
	require.NoError(t, err)
	mm := NewOffline(&config.CLI{AllowedStreams: []string{signer.Streamer()}})
	mm.Access = policy
	for name, ingest := range map[string]func() error{
		"RTMP":         func() error { return mm.RTMPIngest(t.Context(), &RTMPSession{MediaSigner: signer}) },
		"MP4":          func() error { return mm.MP4Ingest(t.Context(), bytes.NewReader(nil), signer) },
		"isolated MP4": func() error { return mm.MP4IngestIsolated(t.Context(), bytes.NewReader(nil), signer) },
		"detached MP4": func() error { return mm.MP4IngestDetached(t.Context(), nil, nil, false, signer) },
		"WHIP": func() error {
			_, err := mm.WHIPIngestDetached(t.Context(), "", signer)
			return err
		},
		"signing pipeline": func() error {
			_, err := mm.SegmentAndSignElem(t.Context(), signer, 1)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) { require.ErrorContains(t, ingest(), "is not allowed") })
	}
}

func publisherAccessForTest(t *testing.T, did string) (*MediaManager, *accessctl.Controller) {
	t.Helper()
	cli := &config.CLI{BroadcasterHost: "node.test", DBURL: ":memory:"}
	state, err := statedb.MakeDB(t.Context(), cli, nil, nil)
	require.NoError(t, err)
	db, err := state.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	b := bus.NewBus()
	controller, err := accessctl.New(t.Context(), access.Bootstrap{
		Authority: "did:web:node.test",
		Entries:   []access.Entry{{Subject: did, Capability: access.Streamer, Effect: access.Allow}},
	}, state, b)
	require.NoError(t, err)
	mm := NewOffline(cli)
	mm.Access = controller
	mm.bus = b
	return mm, controller
}

func revokeAndReapprovePublisher(t *testing.T, controller *accessctl.Controller, did string) {
	t.Helper()
	denial, err := controller.Create(t.Context(), did, access.Streamer, access.Deny, "publisher removed", nil)
	require.NoError(t, err)
	require.NoError(t, controller.Delete(t.Context(), denial.ID))
	require.True(t, controller.Check(did, access.Streamer).Allowed)
}

func TestPublisherReapprovalRejectsPendingDistribution(t *testing.T) {
	did, segment := workerSourceForTest(t, "h264-opus-frag.mp4")
	mm, controller := publisherAccessForTest(t, did)
	ctx, err := mm.admitPublisher(t.Context(), did)
	require.NoError(t, err)
	validated, playable, err := mm.validateSource(ctx, segment, true, did)
	require.NoError(t, err)
	revokeAndReapprovePublisher(t, controller, did)
	require.ErrorContains(t, mm.distributeSegment(context.WithoutCancel(ctx), validated, segment, playable), "reconnect")
	require.Empty(t, mm.BufferedSegments(did), "old pending work cannot enter canonical history after reapproval")
	fresh, err := mm.admitPublisher(t.Context(), did)
	require.NoError(t, err)
	validated, playable, err = mm.validateSource(fresh, segment, true, did)
	require.NoError(t, err)
	require.NoError(t, mm.distributeSegment(fresh, validated, segment, playable))
	require.Len(t, mm.BufferedSegments(did), 1, "a freshly admitted session can publish")
}

func TestPublisherReapprovalRejectsPendingHLS(t *testing.T) {
	did, segment := workerSourceForTest(t, "h264-opus-frag.mp4")
	mm, controller := publisherAccessForTest(t, did)
	ctx, err := mm.admitPublisher(t.Context(), did)
	require.NoError(t, err)
	events, err := unwrapMuxlEvents(ctx, segment)
	require.NoError(t, err)
	mm.feedLiveWindowEvents(ctx, did, events, time.Now(), true)
	state := mm.lockLiveWindow(did, false)
	require.NotNil(t, state)
	track := state.writer.TrackIDs()[0]
	before := len(state.writer.Track(track).Segments)
	release := true
	t.Cleanup(func() {
		if release {
			state.mu.Unlock()
		}
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		mm.feedLiveWindowEvents(context.WithoutCancel(ctx), did, events, time.Now().Add(time.Second), true)
	}()
	waitLiveWindowStack(t, "(*MediaManager).feedLiveWindowEvents", "(*MediaManager).lockLiveWindow")
	revokeAndReapprovePublisher(t, controller, did)
	state.mu.Unlock()
	release = false
	waitLiveWindowWork(t, done)
	require.Len(t, state.writer.Track(track).Segments, before, "revoked pending work cannot enter a live playlist")
}

func TestPublisherAdmissionSurvivesWorkerMetadata(t *testing.T) {
	const did = "did:plc:resumed-publisher"
	mm, controller := publisherAccessForTest(t, did)
	ctx, err := mm.admitPublisher(t.Context(), did)
	require.NoError(t, err)
	generation, err := mm.checkPublisherContext(ctx, did)
	require.NoError(t, err)
	socket := filepath.Join(t.TempDir(), "publisher.sock")
	require.NoError(t, writeWorkerMeta(socket, workerMeta{StreamerDID: did, PublisherGeneration: &generation}))
	meta, err := readWorkerMeta(socket)
	require.NoError(t, err)
	_, err = mm.resumePublisher(t.Context(), meta)
	require.NoError(t, err, "admission survives serialization including generation zero")
	revokeAndReapprovePublisher(t, controller, did)
	_, err = mm.resumePublisher(t.Context(), meta)
	require.ErrorContains(t, err, "reconnect")
	_, err = mm.resumePublisher(t.Context(), workerMeta{StreamerDID: did})
	require.ErrorContains(t, err, "admission metadata")
}

func TestPublisherAccessChangeEndsAdmittedWatcher(t *testing.T) {
	const did = "did:plc:active-publisher"
	mm, controller := publisherAccessForTest(t, did)
	ctx, err := mm.admitPublisher(t.Context(), did)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	revoked := make(chan string, 1)
	go mm.watchKeyRevocation(ctx, did, did, func(reason string) { revoked <- reason })
	waitLiveWindowStack(t, "(*MediaManager).watchKeyRevocation", "key_revocation.go")
	revokeAndReapprovePublisher(t, controller, did)
	select {
	case <-revoked:
	case <-time.After(3 * time.Second):
		t.Fatal("an admitted watcher must end even if access was already restored")
	}
}

func TestPublisherFreshWatcherIgnoresOldAccessChange(t *testing.T) {
	const did = "did:plc:fresh-publisher"
	mm, controller := publisherAccessForTest(t, did)
	revokeAndReapprovePublisher(t, controller, did)
	ctx, err := mm.admitPublisher(t.Context(), did)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	revoked := make(chan string, 1)
	go mm.watchKeyRevocation(ctx, did, did, func(reason string) { revoked <- reason })
	waitLiveWindowStack(t, "(*MediaManager).watchKeyRevocation", "key_revocation.go")
	mm.bus.Publish(access.Topic, access.Change{Revision: 1})
	select {
	case reason := <-revoked:
		t.Fatalf("an old notification revoked a fresh admission: %s", reason)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestIngestWatcherRejectsAlreadyDeniedPublisher(t *testing.T) {
	const did = "did:plc:publisher"
	policy, err := access.NewSnapshot(access.Bootstrap{Authority: "did:web:node.test"}, 1, nil)
	require.NoError(t, err)
	mm := &MediaManager{Access: policy, cli: &config.CLI{AllowedStreams: []string{did}}, bus: bus.NewBus()}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	revoked := make(chan string, 1)
	go mm.watchKeyRevocation(ctx, did, did, func(reason string) { revoked <- reason })
	select {
	case reason := <-revoked:
		require.Contains(t, reason, "publish")
	case <-time.After(2 * time.Second):
		t.Fatal("publisher denial must be checked before waiting for an access-change event")
	}
}

func TestIngestAccessSeparatesPublishingFromCarriage(t *testing.T) {
	streamer, segment := workerSourceForTest(t, "h264-opus-frag.mp4")
	for _, tc := range []struct {
		name      string
		local     bool
		publish   bool
		syndicate bool
		allowed   bool
	}{
		{"local publisher approved", true, true, false, true},
		{"remote carriage does not grant local publishing", true, false, true, false},
		{"remote creator does not need local publishing approval", false, false, true, true},
		{"local publishing does not grant remote carriage", false, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli := &config.CLI{BroadcasterHost: "node.test", AllowedStreams: []string{"did:plc:another-publisher"}}
			if tc.publish {
				cli.AllowedStreams = []string{streamer}
			}
			if tc.syndicate {
				cli.Syndicate = []string{streamer}
			}
			mm := NewOffline(cli)
			policy, err := access.NewSnapshot(access.Bootstrap{
				Authority: "did:web:node.test",
				Modes:     map[access.Capability]access.Mode{access.Streamer: access.Off, access.Syndicate: access.Off},
			}, 1, nil)
			require.NoError(t, err)
			// The static path must work before a runtime controller is installed.
			validated, _, err := mm.validateSource(t.Context(), segment, tc.local, streamer)
			if !tc.allowed {
				require.Error(t, err)
				require.Nil(t, validated)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, validated)
			require.Equal(t, tc.local, validated.local)
			mm.Access = policy
			_, _, err = mm.validateSource(t.Context(), segment, tc.local, streamer)
			require.Error(t, err, "installed policy must override a permissive static fallback")
		})
	}
}

func TestIngestRuntimeCarriageCanOverrideStaticDenial(t *testing.T) {
	streamer, segment := workerSourceForTest(t, "h264-opus-frag.mp4")
	policy, err := access.NewSnapshot(access.Bootstrap{
		Authority: "did:web:node.test",
		Modes:     map[access.Capability]access.Mode{access.Syndicate: access.Allowlist},
		Entries:   []access.Entry{{Subject: streamer, Capability: access.Syndicate, Effect: access.Allow}},
	}, 1, nil)
	require.NoError(t, err)
	mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
	mm.Access = policy
	_, _, err = mm.validateSource(t.Context(), segment, false, streamer)
	require.NoError(t, err)
	_, _, err = mm.validateSource(t.Context(), segment, true, streamer)
	require.Error(t, err, "carriage permission does not admit a local publisher")
}
