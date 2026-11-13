package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	app "github.com/go-gst/go-gst/pkg/gstapp"
	"stream.place/streamplace/pkg/gstpipeline"
	"stream.place/streamplace/pkg/log"
	"stream.place/streamplace/pkg/muxl"
)

// transcodeJob is one fed source segment awaiting its transcoded twin. token is
// opaque caller context (the validated-segment info), returned verbatim to
// onComplete so the caller can archive/distribute the completed segment.
type transcodeJob struct {
	src        []byte
	token      any
	events     []*muxl.MuxlEvent
	data       []byte
	ingredient audioIngredient
}

// streamTranscoder runs ONE long-lived audio transcode for a single stream.
//
// Source segments enter a continuous decode→encode pipeline in arrival order.
// Serialized input markers complete each source with the packets already
// encoded, leaving codec lookahead live. Sources shorter than codec priming
// coalesce until packets are available. Original signed sources remain intact;
// Close delivers retained packets as a separately signed audio addendum.
//
// Lifecycle: created per stream, Feed in order, Close on stream end (flushes the
// tail). Not safe for concurrent Feed; feed from one goroutine (the stream's
// validate path delivers segments in order).
type streamTranscoder struct {
	mm        *MediaManager
	target    string // codec being ADDED: "opus" (source AAC) or "aac" (source Opus)
	sessionID uint64 // ingest-session epoch this transcoder was built for; a newer
	// session rebuilds it (registry-owned: set under transcodersMu before the
	// transcoder is published to the map, read only by needsReset under the same
	// lock, so it needs no separate synchronization).
	signer     muxl.SignerInput
	onComplete func(token any, completed []byte, audioOnly bool)

	ctx    context.Context
	cancel context.CancelFunc
	jobs   chan transcodeJob
	done   chan struct{}

	feedMu sync.Mutex  // serializes Feed so segments enter in order
	reaper *time.Timer // idle reaper; reset on Feed (set by the registry)
	fedSeq int         // per-instance feed counter (debug dump ordering); under feedMu

	mu      sync.Mutex
	started bool
	closed  bool
	err     error
}

// streamTranscoderIdle is how long a per-stream transcoder lingers with no new
// segment before it's flushed and torn down (the stream is presumed ended).
const streamTranscoderIdle = 30 * time.Second

// ingestSessionKey carries the per-ingest-session epoch down the validate path.
// SegmentAndSignElem stamps a fresh epoch on every live session's context (a new
// RTMP/WHIP connection = a new session, with a restarted media timeline);
// feedStreamTranscoder reads it so a reconnect rebuilds the continuous transcoder
// instead of feeding the restarted timeline into the previous session's
// still-running encoder — a large backwards PTS discontinuity that makes the
// encoder stop emitting audio and wedges the stream.
type ingestSessionKey struct{}

func withIngestSession(ctx context.Context, epoch uint64) context.Context {
	return context.WithValue(ctx, ingestSessionKey{}, epoch)
}

// ingestSessionFromContext returns the ingest-session epoch stamped on ctx, or 0
// if none — e.g. a segment replicated from another node (which is already
// dual-codec, so it never builds a transcoder) or a direct unit-test feed.
func ingestSessionFromContext(ctx context.Context) uint64 {
	epoch, _ := ctx.Value(ingestSessionKey{}).(uint64)
	return epoch
}

// feedStreamTranscoder routes one source segment into the stream's continuous
// transcoder, creating it on first use. The completed dual-codec segment is
// distributed asynchronously via distributeSegment when its marker completes.
func (mm *MediaManager) feedStreamTranscoder(ctx context.Context, vs *validatedSegment, src []byte, target string, signer muxl.SignerInput) error {
	did := vs.repoDID
	sessionID := ingestSessionFromContext(ctx)
	mm.transcodersMu.Lock()
	t := mm.transcoders[did]
	if t != nil && sessionID != 0 && sessionID < t.sessionID {
		mm.transcodersMu.Unlock()
		return fmt.Errorf("audio source belongs to an older ingest session")
	}
	if t != nil && (t.needsReset(target, sessionID) || !bytes.Equal(t.signer.CertPEM, signer.CertPEM)) {
		// The live encoder is wrong for the incoming segment:
		//   - a newer ingest session took over — the streamer reconnected (a rapid
		//     stop/start), which restarts the media timeline. Feeding that into the
		//     previous session's continuous encoder is a large backwards PTS jump
		//     that makes it stop emitting audio and wedges the stream;
		//   - the source codec swapped (the needed target flipped, e.g. a streamer
		//     dropped RTMP/AAC and picked up WHIP/Opus), so the pipeline expects the
		//     previous codec;
		//   - the completion signer changed, so a remote source cannot borrow a
		//     local stream key; or
		//   - the pipeline failed.
		// Drain the old encoder before feeding the replacement session.
		old := t
		if old.reaper != nil {
			old.reaper.Stop()
		}
		mm.transcodersMu.Unlock()
		// Keep this instance in the registry while its tail drains, so another
		// feed cannot create a replacement and publish ahead of that tail.
		if err := old.Close(); err != nil {
			log.Error(ctx, "stream transcoder reset close failed", "streamer", did, "error", err)
		}
		mm.transcodersMu.Lock()
		if mm.transcoders[did] == old {
			delete(mm.transcoders, did)
		}
		mm.transcodersMu.Unlock()
		return mm.feedStreamTranscoder(ctx, vs, src, target, signer)
	}
	if t == nil {
		// The transcoder outlives any single request; carry log values but not
		// cancellation, and cancel explicitly on reap.
		// Admission belongs to each completed segment, not the shared encoder.
		// distributeSegment restores a local segment's captured generation;
		// remote segments must not inherit a previous publisher's admission.
		streamCtx := context.WithValue(context.WithoutCancel(ctx), publisherAdmissionKey{}, nil)
		t = mm.newStreamTranscoder(streamCtx, target, signer.CertPEM, signer.KeyPEM, func(token any, completed []byte, audioOnly bool) {
			v := token.(*validatedSegment)
			if audioOnly {
				if err := mm.publishAudioAddendum(context.WithoutCancel(streamCtx), v, completed); err != nil {
					log.Error(streamCtx, "publish audio tail failed", "error", err)
				}
				return
			}
			if err := mm.distributeSegment(context.WithoutCancel(streamCtx), v, completed, nil); err != nil {
				log.Error(streamCtx, "distribute completed segment failed", "streamer", v.repoDID, "error", err)
			}
		})
		// Set before Feed publishes any work to the encoder goroutine. Remote
		// completion must never reuse a local stream's signing authority.
		t.signer = signer
		t.sessionID = sessionID
		t.reaper = time.AfterFunc(streamTranscoderIdle, func() { mm.reapStreamTranscoder(did, t) })
		mm.transcoders[did] = t
		log.Log(ctx, "stream transcoder started", "streamer", did, "target", target, "session", sessionID)
	}
	mm.transcodersMu.Unlock()

	t.reaper.Reset(streamTranscoderIdle)
	return t.Feed(src, vs)
}

// needsReset reports whether an existing per-stream transcoder must be torn down
// and rebuilt rather than fed the incoming segment:
//   - a newer ingest session took over (sessionID advanced — the streamer
//     reconnected, restarting the media timeline; the continuous encoder is still
//     at the old timeline, and a backwards PTS jump would make it stop emitting
//     audio and wedge the stream),
//   - the source codec swapped (target flipped — e.g. RTMP/AAC → WHIP/Opus
//     mid-stream), or
//   - its pipeline has failed.
//
// A stale straggler from an OLDER session (sessionID < t.sessionID) does NOT
// reset — the newer session keeps its encoder. In practice this can't arise: a
// session's segments are all fed (synchronously, before its ingest returns)
// before the next session starts, so feeds never interleave across sessions.
// Guarding on strict advance rather than inequality just makes that explicit and
// avoids any reset thrash if they ever did.
func (t *streamTranscoder) needsReset(target string, sessionID uint64) bool {
	return t.target != target || sessionID > t.sessionID || t.failed() || t.isClosed()
}

// failed reports whether the transcoder's pipeline has errored out.
func (t *streamTranscoder) failed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err != nil
}

// isClosed reports whether the transcoder has been torn down (flushed + stopped).
func (t *streamTranscoder) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// reapStreamTranscoder flushes and removes an idle stream's transcoder (the
// flush emits its final buffered segment). Safe to call once per timer fire.
func (mm *MediaManager) reapStreamTranscoder(did string, t *streamTranscoder) {
	if err := t.Close(); err != nil {
		log.Error(t.ctx, "stream transcoder reap close failed", "streamer", did, "error", err)
	}
	mm.transcodersMu.Lock()
	if mm.transcoders[did] == t {
		delete(mm.transcoders, did)
	}
	mm.transcodersMu.Unlock()
}

// closeStreamTranscoder drains only this ingest session. A disconnected older
// publisher must not flush the replacement session's continuous encoder.
func (mm *MediaManager) closeStreamTranscoder(ctx context.Context, did string) error {
	mm.transcodersMu.Lock()
	transcoder := mm.transcoders[did]
	if transcoder == nil || transcoder.sessionID != ingestSessionFromContext(ctx) {
		mm.transcodersMu.Unlock()
		return nil
	}
	mm.transcodersMu.Unlock()
	err := transcoder.Close()
	mm.transcodersMu.Lock()
	if mm.transcoders[did] == transcoder {
		delete(mm.transcoders, did)
	}
	mm.transcodersMu.Unlock()
	return err
}

// newStreamTranscoder starts a per-stream continuous transcoder. target is the
// codec to add. cert/keyPEM initialize the S2PA signer. onComplete is invoked,
// in order, with each completed dual-codec segment (and its feed token).
func (mm *MediaManager) newStreamTranscoder(parent context.Context, target string, cert, keyPEM []byte, onComplete func(token any, completed []byte, audioOnly bool)) *streamTranscoder {
	ctx, cancel := context.WithCancel(parent)
	t := &streamTranscoder{
		mm: mm, target: target, signer: muxl.SignerInput{CertPEM: cert, KeyPEM: keyPEM}, onComplete: onComplete,
		ctx: ctx, cancel: cancel,
		jobs: make(chan transcodeJob, 16), done: make(chan struct{}),
	}
	go func() {
		defer close(t.done)
		err := t.run()
		t.mu.Lock()
		if t.err == nil {
			t.err = err
		}
		t.mu.Unlock()
		t.cancel() // release a feed or native callback after failure
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Error(ctx, "stream transcoder exited", "target", target, "error", err)
		}
	}()
	return t
}

// Feed pushes a source segment (bare canonical .m4s) in arrival order. The
// matching completion is delivered to onComplete after its serialized marker.
func (t *streamTranscoder) Feed(src []byte, token any) error {
	t.feedMu.Lock()
	defer t.feedMu.Unlock()

	// Debug: capture each source segment fed to THIS transcoder instance, in
	// feed order, so a wedging input sequence can be replayed into a
	// streamTranscoder in a test. No-op unless --segment-debug-dir is set. The
	// per-instance sequence number is baked into the name (not just the dump's
	// async wall-clock stamp) so a burst of sub-second feeds still sorts in feed
	// order. Numbering is per-instance, so the run that actually wedges (before
	// a reset rebuilds a fresh transcoder) is a self-contained 0..N sequence.
	seq := t.fedSeq
	t.fedSeq++
	t.mm.cli.DumpDebugSegment(t.ctx, fmt.Sprintf("transcoder-feed-%s-%05d.m4s", t.target, seq), bytes.NewReader(src))

	t.mu.Lock()
	switch {
	case t.closed:
		t.mu.Unlock()
		return fmt.Errorf("stream transcoder closed")
	case t.err != nil:
		err := t.err
		t.mu.Unlock()
		return err
	}
	first := !t.started
	t.mu.Unlock()
	// Native input selects one video track and the audio it decodes.
	// The queued job retains every original signed source track unchanged.
	events, err := unwrapMuxlEvents(t.ctx, src)
	if err != nil {
		return fmt.Errorf("unwrap audio completion source: %w", err)
	}
	ingredient, err := sourceAudioIngredient(events)
	if err != nil {
		return err
	}
	if source, ok := token.(*validatedSegment); ok {
		ingredient.policy = source.meta
		ingredient.manifest = source.manifest
	}
	if ingredient.manifest == nil {
		verified, err := ValidateMP4MediaC2PA(t.ctx, ingredient.data)
		if err != nil {
			return fmt.Errorf("read source audio policy: %w", err)
		}
		ingredient.manifest = verified.Manifest
		if ingredient.policy == nil {
			ingredient.policy = verified.Meta
		}
	}
	selected, err := selectEventTracks(events, 0, "", t.target == "aac")
	if err != nil {
		return fmt.Errorf("select audio completion tracks: %w", err)
	}
	var init []byte
	if first {
		var header bytes.Buffer
		if err := muxl.RunMuxlWrapInit(t.ctx, bytes.NewReader(selected), &header); err != nil {
			return fmt.Errorf("synthesize transcoder init: %w", err)
		}
		init = header.Bytes()
	}

	data := selected
	if len(init) > 0 {
		data = append(init, selected...)
	}
	select {
	case t.jobs <- transcodeJob{src: src, token: token, events: events, ingredient: ingredient, data: data}:
	case <-t.ctx.Done():
		return t.ctx.Err()
	}
	t.mu.Lock()
	t.started = true
	t.mu.Unlock()

	return nil
}

// Close flushes the tail (EOS → final segment) and tears the pipeline down.
func (t *streamTranscoder) Close() error {
	t.feedMu.Lock()
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		close(t.jobs)
	}
	t.mu.Unlock()
	t.feedMu.Unlock()
	if t.reaper != nil {
		t.reaper.Stop()
	}
	select {
	case <-t.done:
	case <-time.After(15 * time.Second):
		t.cancel()
		<-t.done
	}
	t.mu.Lock()
	err := t.err
	t.mu.Unlock()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
		return err
	}
	return nil
}

// run preserves the continuous codec state. Serialized input markers close
// the packets already encoded for a source, without draining the codec tail.
func (t *streamTranscoder) run() error {
	ctx := t.ctx
	var first transcodeJob
	select {
	case job, ok := <-t.jobs:
		if !ok {
			return nil
		}
		first = job
	case <-ctx.Done():
		return ctx.Err()
	}
	pipeline, err := buildAudioTranscodePipeline(t.target)
	if err != nil {
		return err
	}
	defer func() { t.cancel(); pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone) }()
	output := make(chan audioOutput, 128)
	emit := func(item audioOutput) bool {
		select {
		case output <- item:
			return true
		case <-ctx.Done():
			return false
		}
	}
	installAudioOutput(pipeline, emit)
	src := pipeline.GetByName("src").(app.AppSrc)
	initial := true
	src.SetCallbacks(&app.AppSrcCallbacks{NeedData: func(src app.AppSrc, _ uint) {
		var job transcodeJob
		if initial {
			job = first
			first = transcodeJob{}
			initial = false
		} else {
			select {
			case next, ok := <-t.jobs:
				if !ok {
					src.EndOfStream()
					return
				}
				job = next
			case <-ctx.Done():
				src.EndOfStream()
				return
			}
		}
		if !emit(audioOutput{job: &job}) {
			return
		}
		buffer, err := gstpipeline.NewBufferFromBytes(job.data)
		if err != nil {
			src.PostMessage(gst.NewMessageError(src, "copy audio input", err))
			return
		}
		if src.PushBuffer(buffer) != gst.FlowOK {
			return
		}
		if !src.SendEvent(gst.NewEventCustom(gst.EventCustomDownstream, gst.NewStructureEmpty(audioBoundaryEvent))) {
			src.PostMessage(gst.NewMessageError(src, "queue audio boundary", fmt.Errorf("boundary event rejected")))
		}
	}})
	nativeDone := make(chan error, 1)
	go func() { nativeDone <- gstpipeline.HandleBusMessages(ctx, pipeline) }()
	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		return fmt.Errorf("stream transcode: set playing failed")
	}
	var jobs []transcodeJob
	var packets []audioPacket
	var header []byte
	var last *transcodeJob
	var ingredients []audioIngredient
	nativeEnded := false
	finish := func(job *transcodeJob, tail bool) error {
		if len(packets) == 0 {
			return fmt.Errorf("audio boundary has no encoded packets")
		}
		if len(header) == 0 {
			return fmt.Errorf("audio boundary has no initialization segment")
		}
		// Retain the preceding source as well as every source whose samples can
		// overlap this chunk. A codec packet may straddle an input boundary.
		for len(ingredients) > 1 && ingredients[1].end <= packets[0].pts {
			ingredients = ingredients[1:]
		}
		private := false
		current := job.ingredient.policy
		for _, ingredient := range ingredients {
			policy := ingredient.policy
			if !policy.Published || !reflect.DeepEqual(policy.ContentWarnings, current.ContentWarnings) || !reflect.DeepEqual(policy.ContentRights, current.ContentRights) || !reflect.DeepEqual(policy.DistributionPolicy, current.DistributionPolicy) || !reflect.DeepEqual(policy.MetadataConfiguration, current.MetadataConfiguration) {
				private = true
			}
		}
		title := "transcoded audio"
		if tail {
			title = audioTailTitle
		}
		assertions := []obj{transcodeActions()}
		// A stream-authorized derivative carries the same metadata gates as
		// the source, alongside its own transcoded action and signed ingredients.
		for _, assertion := range job.ingredient.manifest.Assertions {
			switch assertion.Label {
			case StreamplaceMetadata, "place.stream.metadata.configuration", "place.stream.livestream", "cawg.training-mining":
				assertions = append(assertions, obj{"label": assertion.Label, "data": assertion.Data})
			}
		}
		if current.Published && !private {
			actions := assertions[0]["data"].(obj)["actions"].([]obj)
			assertions[0]["data"].(obj)["actions"] = append(actions, obj{"action": C2PAPublishedAction})
		}
		if private {
			assertions = append(assertions, obj{"label": audioPublicationAssertion, "data": obj{"published": false}})
		}
		manifest, err := json.Marshal(obj{"title": title, "assertions": assertions})
		if err != nil {
			return err
		}
		signed, err := t.mm.signAudioChunk(ctx, header, packets, ingredients, job.events, t.signer, manifest)
		if err != nil {
			return err
		}
		token := job.token
		if private {
			if source, ok := token.(*validatedSegment); ok {
				restricted := *source
				metadata := *source.meta
				metadata.Published = false
				restricted.meta = &metadata
				token = &restricted
			}
		}
		if tail {
			t.onComplete(token, signed, true)
		} else {
			completed := make([]byte, 0, len(job.src)+len(signed))
			completed = append(completed, job.src...)
			completed = append(completed, signed...)
			t.onComplete(token, completed, false)
		}
		packets = nil
		return nil
	}
	ready, boundaryPackets := 0, 0
	finishReady := func() error {
		if ready == 0 || boundaryPackets == 0 || len(header) == 0 {
			return nil
		}
		job := jobs[0]
		if ready > 1 {
			// A scene-cut GoP can be shorter than codec priming. Retain its original
			// signed bytes and combine only pending sources until audio is encodable.
			var combined []byte
			for _, source := range jobs[:ready] {
				combined = append(combined, source.src...)
			}
			job.src = combined
			if source, ok := job.token.(*validatedSegment); ok {
				valid, err := ValidateMP4Media(ctx, combined)
				if err != nil {
					return err
				}
				merged := *source
				merged.mediaData = valid.MediaData
				job.token = &merged
			}
		}
		available := packets
		packets = available[:boundaryPackets]
		if boundaryPackets < len(available) {
			// The retained packet supplies the preceding packet's final duration,
			// including native timestamp corrections, before that packet is signed.
			previous := &packets[len(packets)-1]
			next := available[boundaryPackets].pts
			if next <= previous.pts {
				return fmt.Errorf("encoded audio timestamps are not increasing")
			}
			previous.duration = next - previous.pts
		}
		if err := finish(&job, false); err != nil {
			return err
		}
		clear(available[:boundaryPackets])
		packets = available[boundaryPackets:]
		boundaryPackets = 0
		clear(jobs[:ready])
		jobs = jobs[ready:]
		ready = 0
		last = &job
		return nil
	}
	packetEOS, headerEOS := false, false
	for {
		select {
		case item := <-output:
			switch {
			case item.err != nil:
				return item.err
			case item.job != nil:
				if len(jobs) >= cap(t.jobs) {
					return fmt.Errorf("audio encoder did not complete its pending sources")
				}
				jobs = append(jobs, *item.job)
			case item.header != nil:
				header = item.header
				if err := finishReady(); err != nil {
					return err
				}
			case item.packet != nil:
				packets = append(packets, *item.packet)
			case item.boundary:
				if ready >= len(jobs) {
					return fmt.Errorf("audio boundary has no source")
				}
				ingredients = append(ingredients, jobs[ready].ingredient)
				ready++
				// Keep one encoded packet so the final signed duration is known
				// from the next PTS, without waiting for another source GoP.
				boundaryPackets = max(len(packets)-1, 0)
				if err := finishReady(); err != nil {
					return err
				}
			case item.eos:
				packetEOS = true
			case item.muxEOS:
				headerEOS = true
			}
			// The header muxer may emit its initialization only while draining a
			// short input. The direct packet sink's EOS alone cannot end this run.
			if packetEOS && headerEOS {
				if ready != len(jobs) {
					return fmt.Errorf("audio ended before source boundaries")
				}
				if ready > 0 {
					boundaryPackets = len(packets)
					// An initial sub-priming source can produce everything at EOS. Keep
					// its final packet in the continuation so live HLS also flushes the
					// final short fragments, without marking a resumable playlist as VOD.
					if boundaryPackets > 1 {
						boundaryPackets--
					}
				}
				if err := finishReady(); err != nil {
					return err
				}
				if len(jobs) > 0 {
					return fmt.Errorf("audio source ended without encoded output")
				}
				if len(packets) > 0 && last != nil {
					if err := finish(last, true); err != nil {
						return err
					}
				}
				if !nativeEnded {
					return <-nativeDone
				}
				return nil
			}
		case err := <-nativeDone:
			if err != nil {
				return err
			}
			nativeEnded = true
		case <-ctx.Done():
			return ctx.Err()
		}
	}

}

// audioTrackID returns the (single) audio rendition's track id in a catalog.
func audioTrackID(cat *muxl.MuxlCatalog) (uint32, bool) {
	if cat == nil || cat.Audio == nil {
		return 0, false
	}
	for _, a := range cat.Audio.Renditions {
		return a.TrackID(), true
	}
	return 0, false
}
