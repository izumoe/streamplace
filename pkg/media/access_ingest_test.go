package media

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/accessctl"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/crypto/signers"
	"stream.place/streamplace/pkg/statedb"
)

func TestPublisherDenialPrecedesIngestSetup(t *testing.T) {
	signer := newBareSegmentSigner(t)
	signer.StreamerName = signer.DID()
	policy, err := access.NewSnapshot(access.Bootstrap{Authority: "did:web:node.test"}, 1, nil, nil)
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

func publisherAccessForTest(t *testing.T, did string, extra ...access.Entry) (*MediaManager, *accessctl.Controller) {
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
		Modes:     map[access.Capability]access.Mode{access.Syndicate: access.Allowlist},
		Entries:   append([]access.Entry{{Subject: did, Capability: access.Streamer, Effect: access.Allow}}, extra...),
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
	done := make(chan error, 1)
	go func() {
		done <- mm.FeedLiveRenditions(context.WithoutCancel(ctx), did, segment, time.Now().Add(time.Second), true)
	}()
	waitLiveWindowStack(t, "(*MediaManager).feedLiveWindowEvents", "(*MediaManager).lockLiveWindow")
	revokeAndReapprovePublisher(t, controller, did)
	state.mu.Unlock()
	release = false
	select {
	case err := <-done:
		require.ErrorContains(t, err, "reconnect", "the caller must know its pending rendition was rejected before publishing elsewhere")
	case <-time.After(3 * time.Second):
		t.Fatal("pending rendition feed did not finish")
	}
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
	policy, err := access.NewSnapshot(access.Bootstrap{Authority: "did:web:node.test"}, 1, nil, nil)
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
			}, 1, nil, nil)
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
	}, 1, nil, nil)
	require.NoError(t, err)
	mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
	mm.Access = policy
	_, _, err = mm.validateSource(t.Context(), segment, false, streamer)
	require.NoError(t, err)
	_, _, err = mm.validateSource(t.Context(), segment, true, streamer)
	require.Error(t, err, "carriage permission does not admit a local publisher")
}

func TestRemoteCompletionDoesNotInheritRevokedPublisher(t *testing.T) {
	signer := newBareSegmentSigner(t)
	signer.StreamerName = signer.DID()
	did := signer.Streamer()
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(signer.PrebuiltManifest, &manifest))
	assertions := manifest["assertions"].([]any)
	assertions[1].(map[string]any)["data"].(map[string]any)["dc:creator"] = did
	assertions[0].(map[string]any)["data"].(map[string]any)["actions"] = []any{
		map[string]any{"action": "c2pa.created"}, map[string]any{"action": "c2pa.published"},
	}
	var err error
	signer.PrebuiltManifest, err = json.Marshal(manifest)
	require.NoError(t, err)
	segments := allSignedBareSegments(t, t.Context(), signer, getFixture("h264-opus-frag.mp4"))
	require.GreaterOrEqual(t, len(segments), 2)
	mm, controller := publisherAccessForTest(t, did, access.Entry{Subject: did, Capability: access.Syndicate, Effect: access.Allow})
	mm.transcoders = map[string]*streamTranscoder{}
	previousVerbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "1"))
	defer func() { require.NoError(t, flag.Set("v", previousVerbosity)) }()
	var logs logCapture
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	ctx, err := mm.admitPublisher(t.Context(), did)
	require.NoError(t, err)
	key, err := signers.MarshalES256KPrivateKeyPEM(signer.Signer)
	require.NoError(t, err)
	local, _, err := mm.validateSource(ctx, segments[0], true, did)
	require.NoError(t, err)
	require.NoError(t, mm.feedStreamTranscoder(ctx, local, segments[0], "aac", signer.Cert, key))
	transcoder := mm.transcoders[did]
	t.Cleanup(func() { require.NoError(t, transcoder.Close()) })
	revokeAndReapprovePublisher(t, controller, did)
	for _, segment := range segments[1:] {
		remote, _, err := mm.validateSource(t.Context(), segment, false, did)
		require.NoError(t, err)
		require.NoError(t, mm.feedStreamTranscoder(t.Context(), remote, segment, "aac", signer.Cert, key))
	}
	require.Same(t, transcoder, mm.transcoders[did], "remote carriage can reuse the same codec pipeline")
	require.NoError(t, transcoder.Close())
	require.Contains(t, logs.String(), "must reconnect after access changed", "the revoked local completion is rejected explicitly")
	buffered := mm.BufferedSegments(did)
	require.Len(t, buffered, len(segments)-1, "revoked local completion stays rejected while remote completions remain eligible")
	for _, segment := range buffered {
		require.False(t, segment.Local)
	}
	require.Eventually(t, func() bool {
		window, _ := mm.GetLiveWindow(did)
		return window != nil
	}, 3*time.Second, time.Millisecond, "remote completion must enter HLS independently of the revoked local admission")
}

func TestRevokedFirstHLSFeedLeavesNoWindow(t *testing.T) {
	did, segment := workerSourceForTest(t, "h264-opus-frag.mp4")
	mm, controller := publisherAccessForTest(t, did)
	ctx, err := mm.admitPublisher(t.Context(), did)
	require.NoError(t, err)
	revokeAndReapprovePublisher(t, controller, did)
	require.ErrorContains(t, mm.FeedLiveRenditions(ctx, did, segment, time.Now(), true), "reconnect")
	window, published := mm.GetLiveWindow(did)
	require.Nil(t, window)
	require.False(t, published)
}

func TestPublisherModeReopeningRejectsPendingDistribution(t *testing.T) {
	did, segment := workerSourceForTest(t, "h264-opus-frag.mp4")
	mm, controller := publisherAccessForTest(t, did)
	ctx, err := mm.admitPublisher(t.Context(), did)
	require.NoError(t, err)
	validated, playable, err := mm.validateSource(ctx, segment, true, did)
	require.NoError(t, err)
	off := access.Off
	require.NoError(t, controller.SetMode(t.Context(), access.Streamer, &off, controller.Snapshot().Revision()))
	require.NoError(t, controller.SetMode(t.Context(), access.Streamer, nil, controller.Snapshot().Revision()))
	require.ErrorContains(t, mm.distributeSegment(context.WithoutCancel(ctx), validated, segment, playable), "reconnect")
	require.Empty(t, mm.BufferedSegments(did))
	fresh, err := mm.admitPublisher(t.Context(), did)
	require.NoError(t, err)
	validated, playable, err = mm.validateSource(fresh, segment, true, did)
	require.NoError(t, err)
	require.NoError(t, mm.distributeSegment(fresh, validated, segment, playable))
}

func TestCarriageUsesSavedPolicyOverStartupDefaults(t *testing.T) {
	const did = "did:plc:carried"
	mm, controller := publisherAccessForTest(t, did)
	require.False(t, mm.ShouldSyndicate(did))
	open := access.Open
	require.NoError(t, controller.SetMode(t.Context(), access.Syndicate, &open, controller.Snapshot().Revision()))
	require.True(t, mm.ShouldSyndicate(did))
	denial, err := controller.Create(t.Context(), did, access.Syndicate, access.Deny, "local ban", nil)
	require.NoError(t, err)
	require.False(t, mm.ShouldSyndicate(did))
	require.NoError(t, controller.Delete(t.Context(), denial.ID))
	require.True(t, mm.ShouldSyndicate(did))
	require.NoError(t, controller.SetMode(t.Context(), access.Syndicate, nil, controller.Snapshot().Revision()))
	require.False(t, mm.ShouldSyndicate(did))
}

func TestDisabledSyndicationOverridesSavedOpenPolicy(t *testing.T) {
	mm, controller := publisherAccessForTest(t, "did:plc:carried")
	open := access.Open
	require.NoError(t, controller.SetMode(t.Context(), access.Syndicate, &open, controller.Snapshot().Revision()))
	mm.cli.DisableSyndication = true
	require.False(t, mm.ShouldSyndicate("did:plc:carried"))
}

func TestCarriageRestrictionRejectsPendingDistribution(t *testing.T) {
	did, segment := workerSourceForTest(t, "h264-opus-frag.mp4")
	mm, controller := publisherAccessForTest(t, did, access.Entry{Subject: did, Capability: access.Syndicate, Effect: access.Allow})
	validated, playable, err := mm.validateSource(t.Context(), segment, false, "")
	require.NoError(t, err)
	off := access.Off
	require.NoError(t, controller.SetMode(t.Context(), access.Syndicate, &off, controller.Snapshot().Revision()))
	require.ErrorContains(t, mm.distributeSegment(t.Context(), validated, segment, playable), "does not syndicate")
	require.Empty(t, mm.BufferedSegments(did))
}
