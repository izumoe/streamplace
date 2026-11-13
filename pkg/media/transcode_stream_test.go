package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/aqtime"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/crypto/signers"
	"stream.place/streamplace/pkg/muxl"
	"stream.place/streamplace/test/remote"
)

// TestStreamTranscoderNeedsReset covers the rebuild triggers for the per-DID
// continuous transcoder: a newer ingest session (a streamer reconnecting — a
// rapid stop/start that restarts the media timeline), a source-codec swap (RTMP/
// AAC ↔ WHIP/Opus flips the needed target), or a failed pipeline. In each case
// the live encoder is wrong for the incoming segment and must be torn down
// rather than fed (feeding a restarted timeline into it wedges the stream).
func TestStreamTranscoderNeedsReset(t *testing.T) {
	tr := &streamTranscoder{target: "opus", sessionID: 1} // adding Opus to an AAC source

	// Same codec + same session: keep the continuous encoder running.
	require.False(t, tr.needsReset("opus", 1), "same codec + same session keeps the encoder running")

	// Source codec swapped mid-session (AAC→Opus flips the needed target): reset.
	require.True(t, tr.needsReset("aac", 1), "source codec swapped (AAC→Opus) must reset")

	// A newer ingest session took over (streamer reconnected → restarted media
	// timeline): reset even with the same codec — the rapid-restart fix.
	require.True(t, tr.needsReset("opus", 2), "a newer ingest session must rebuild the transcoder")

	// A stale straggler from an OLDER session must NOT reset the newer encoder.
	require.False(t, tr.needsReset("opus", 0), "an older/stale session must not reset the current encoder")

	// A failed pipeline rebuilds on the next segment regardless.
	failed := &streamTranscoder{target: "aac", sessionID: 5, err: errors.New("pipeline died")}
	require.True(t, failed.needsReset("aac", 5), "a failed pipeline rebuilds on the next segment")
}

// TestFeedStreamTranscoderRebuildsOnNewSession is the end-to-end regression for
// the rapid stop/start wedge: a streamer disconnects and reconnects within the
// transcoder's idle window, so the registry would otherwise feed the second
// session's restarted media timeline into the first session's still-running
// continuous encoder (a large backwards PTS jump → the encoder stops emitting
// audio → "emitted segment missing audio track" → segments dropped → the stream
// wedges). With the ingest-session epoch, the second session must get a FRESH
// transcoder and the first session's must be flushed + torn down.
//
// It drives the real registry path (feedStreamTranscoder, the same one
// ValidateMP4 uses): two sessions over the same DID + codec, with the same
// fixture re-fed for the second session — re-feeding restarts the source PTS at
// zero, exactly the discontinuity a reconnect produces.
func TestFeedStreamTranscoderRebuildsOnNewSession(t *testing.T) {
	ctx := context.Background()
	ms := newBareSegmentSigner(t)
	segs := allSignedBareSegments(t, ctx, ms, getFixture("h264-opus-frag.mp4"))
	require.GreaterOrEqual(t, len(segs), 2, "fixture should produce multiple segments")

	keyPEM, err := signers.MarshalES256KPrivateKeyPEM(ms.Signer)
	require.NoError(t, err)

	// A real-enough MediaManager: a temp data dir so completed segments archive
	// without touching the repo, and an (unused) live-window map. Completed
	// segments are unpublished, so distributeSegment archives them but folds
	// nothing into the live window and notifies no subscribers — no blocking.
	mm := &MediaManager{
		cli:         &config.CLI{BroadcasterHost: "test.example.com", DataDir: t.TempDir()},
		transcoders: map[string]*streamTranscoder{},
		liveWindows: map[string]*liveWindowState{},
	}

	const did = "did:web:didweb.example"
	base := time.Unix(1700000000, 0).UTC()
	feedSession := func(epoch uint64, startIdx int) {
		sctx := withIngestSession(ctx, epoch)
		for i, seg := range segs {
			// Distinct per-segment StartTime so archived filenames don't collide.
			vs := &validatedSegment{
				repoDID: did,
				meta:    &SegmentMetadata{StartTime: aqtime.FromTime(base.Add(time.Duration(startIdx+i) * time.Second))},
				local:   true,
			}
			require.NoError(t, mm.feedStreamTranscoder(sctx, vs, seg, "aac", muxl.SignerInput{CertPEM: ms.Cert, KeyPEM: keyPEM}),
				"feed session %d segment %d", epoch, i)
		}
	}

	// Session 1.
	s1 := mm.nextIngestSession()
	feedSession(s1, 0)
	t1 := mm.transcoders[did]
	require.NotNil(t, t1, "session 1 built a transcoder")
	require.Equal(t, s1, t1.sessionID)

	// Session 2: same DID + codec, fresh epoch (the reconnect). The first feed of
	// this session must reset the registry to a brand-new transcoder.
	s2 := mm.nextIngestSession()
	feedSession(s2, len(segs))
	t2 := mm.transcoders[did]
	require.NotNil(t, t2, "session 2 built a transcoder")
	require.NotSame(t, t1, t2,
		"a new ingest session must rebuild the transcoder, not reuse the previous session's continuous encoder")
	require.Equal(t, s2, t2.sessionID)

	// The previous session's transcoder is drained before its replacement starts.
	require.Eventually(t, t1.isClosed, 20*time.Second, 20*time.Millisecond,
		"the previous session's transcoder must be flushed + torn down on reconnect")

	require.NoError(t, t2.Close(), "the rebuilt transcoder drains cleanly")
}

// allSignedBareSegments signs the fragmented fixture per-segment and returns
// every GoP's bare canonical .m4s (the live ingest shape), in order.
func allSignedBareSegments(t testing.TB, ctx context.Context, ms *MediaSignerLocal, fragPath string) [][]byte {
	t.Helper()
	frag, err := os.ReadFile(fragPath)
	require.NoError(t, err)
	eventCh := make(chan *muxl.MuxlEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		err := ms.SignSegmentStream(ctx, bytes.NewReader(frag), eventCh, nil)
		close(eventCh)
		errCh <- err
	}()
	var out [][]byte
	for ev := range eventCh {
		if ev.Type == "signed-segment" {
			out = append(out, concatTracksSorted(ev.Tracks))
		}
	}
	require.NoError(t, <-errCh)
	return out
}

// audioDurationsSeconds re-segments a bare .m4s and returns its Opus and AAC
// audio-track durations in seconds (0 if absent).
func audioDurationsSeconds(t *testing.T, ctx context.Context, seg []byte) (opusSec, aacSec float64) {
	t.Helper()
	var fmp4 bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(seg), "fmp4", &fmp4))
	events, err := segmentMuxlEvents(ctx, fmp4.Bytes())
	require.NoError(t, err)
	cat, _ := catalogAndTracks(events)
	require.NotNil(t, cat)

	type trackInfo struct {
		codec string
		ts    uint32
	}
	info := map[string]trackInfo{}
	if cat.Audio != nil {
		for _, a := range cat.Audio.Renditions {
			info[strconv.FormatUint(uint64(a.TrackID()), 10)] = trackInfo{a.Codec, a.Timescale()}
		}
	}
	for _, ev := range events {
		if ev.Type != "segment" && ev.Type != "signed-segment" {
			continue
		}
		for tid, dur := range ev.Durations {
			x, ok := info[tid]
			if !ok || x.ts == 0 {
				continue
			}
			sec := float64(dur) / float64(x.ts)
			switch {
			case isOpusCodec(x.codec):
				opusSec += sec
			case isAACCodec(x.codec):
				aacSec += sec
			}
		}
	}
	return opusSec, aacSec
}

func TestAudioCompletionSignsEveryCanonicalPiece(t *testing.T) {
	ctx := t.Context()
	ms := newBareSegmentSigner(t)
	pieces := allSignedBareSegments(t, ctx, ms, getFixture("h264-opus-frag.mp4"))
	require.GreaterOrEqual(t, len(pieces), 2)
	source := bytes.Join(pieces, nil)
	sourceEvents, err := unwrapMuxlEvents(ctx, source)
	require.NoError(t, err)
	ingredient, err := sourceAudioIngredient(sourceEvents)
	require.NoError(t, err)
	audioID, ok := audioTrackID(catalogFromEvents(sourceEvents))
	require.True(t, ok)
	// Use real consecutive packets and codec initialization. The chunk writer
	// receives track 1 from its encoder's muxer, independently of source IDs.
	var audio bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(ingredient.data), "fmp4", &audio))
	canonical, err := muxl.RunMuxlCanonicalize(ctx, audio.Bytes(), map[uint32]uint32{audioID: 1})
	require.NoError(t, err)
	var header bytes.Buffer
	require.NoError(t, muxl.RunMuxlWrapInit(ctx, bytes.NewReader(canonical), &header))
	packets := canonicalAudioPackets(t, ctx, canonical, "opus")
	require.NotEmpty(t, packets)
	cert, key := nodeSignerForTest(t)
	mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "test.example.com"}}
	signed, err := mm.signAudioChunk(ctx, header.Bytes(), packets, []audioIngredient{ingredient}, sourceEvents, muxl.SignerInput{CertPEM: cert, KeyPEM: key}, transcodeManifestFor(mm.cli.BroadcasterDID(), "transcoded audio"))
	require.NoError(t, err)
	verification, err := muxl.RunMuxlVerify(ctx, bytes.NewReader(signed))
	require.NoError(t, err)
	assets, err := parseVerifiedSegments([]byte(verification))
	require.NoError(t, err, "each derived canonical audio asset must carry its own valid signature")
	require.GreaterOrEqual(t, len(assets), 2, "the chunk must cross a canonical audio boundary and sign every piece")
	require.Equal(t, packets, canonicalAudioPackets(t, ctx, signed, "opus"), "every packet survives canonicalization and signing")
	for _, test := range []struct {
		name        string
		first, next uint64
	}{
		{"equal clock", uint64(time.Second), uint64(time.Second)},
		{"backwards clock", uint64(time.Second), uint64(time.Second / 2)},
		{"duration overflow", 0, uint64(time.Second) * (uint64(^uint32(0))/48000 + 2)},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := append([]audioPacket(nil), packets[:2]...)
			invalid[0].pts, invalid[1].pts = test.first, test.next
			_, err := mm.signAudioChunk(ctx, header.Bytes(), invalid, []audioIngredient{ingredient}, sourceEvents, muxl.SignerInput{CertPEM: cert, KeyPEM: key}, transcodeManifestFor(mm.cli.BroadcasterDID(), "transcoded audio"))
			require.ErrorContains(t, err, "invalid sample duration", "invalid clocks must not be silently truncated into an MP4 sample table")
		})
	}
}

// TestStreamTranscoderGapless feeds every segment of one stream through the
// continuous transcoder and checks the result: each completed segment carries
// both codecs and verifies, and the transcoded (AAC) track's total duration
// matches the source (Opus) track's — i.e. it is NOT inflated by per-segment
// encoder priming, which is the whole point of running one continuous encoder.
func TestStreamTranscoderGapless(t *testing.T) {
	ctx := context.Background()
	ms := newBareSegmentSigner(t)
	segs := allSignedBareSegments(t, ctx, ms, getFixture("h264-opus-frag.mp4"))
	require.GreaterOrEqual(t, len(segs), 2, "fixture should produce multiple segments to test continuity")

	mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "test.example.com"}}
	keyPEM, err := signers.MarshalES256KPrivateKeyPEM(ms.Signer)
	require.NoError(t, err)

	var mu sync.Mutex
	var completed [][]byte
	tr := mm.newStreamTranscoder(ctx, "aac", ms.Cert, keyPEM, func(_ any, c []byte, audioOnly bool) {
		mu.Lock()
		appendAudioCompletion(&completed, c, audioOnly)
		mu.Unlock()
	})

	for i, s := range segs {
		require.NoError(t, tr.Feed(s, i), "feed segment %d", i)
	}
	require.NoError(t, tr.Close())

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, completed, "transcoder emitted completed segments")

	var totalOpus, totalAAC float64
	for i, c := range completed {
		codecs := audioCodecsOf(t, ctx, c)
		hasOpus, hasAAC := false, false
		for _, cc := range codecs {
			if isOpusCodec(cc) {
				hasOpus = true
			}
			if isAACCodec(cc) {
				hasAAC = true
			}
		}
		require.True(t, hasOpus, "segment %d keeps the source Opus track (got %v)", i, codecs)
		require.True(t, hasAAC, "segment %d gains an AAC track (got %v)", i, codecs)

		out, err := muxl.RunMuxlVerify(ctx, bytes.NewReader(c))
		require.NoError(t, err, "segment %d verify", i)
		require.NotContains(t, out, `"validation_state":"Invalid"`, "segment %d must validate", i)

		o, a := audioDurationsSeconds(t, ctx, c)
		// Canonicalization can split an audio chunk into multiple assets. Keeping
		// only the first would truncate playback; this guards against gross
		// under-fill independently of the number of completed source segments.
		require.Greater(t, a, 0.65*o,
			"segment %d: transcoded AAC %.3fs far short of source Opus %.3fs (halved track?)", i, a, o)
		totalOpus += o
		totalAAC += a
	}

	t.Logf("completed=%d totalOpus=%.3fs totalAAC=%.3fs delta=%.3fs",
		len(completed), totalOpus, totalAAC, totalAAC-totalOpus)
	// Gapless: the transcoded track tracks the source duration with no per-segment
	// accumulation. The continuous encoder pays its priming ONCE at stream start
	// (the gappy per-segment transcoder this replaced paid 40–80 ms at EVERY
	// boundary). Input markers regroup whole encoded packets, so per-segment
	// durations quantize by an AAC frame (~21 ms) without accumulating drift.
	// The fixture is only a few short segments, too few to
	// average the one-time priming, so bound = priming + ~one frame per segment.
	tol := 0.040 + 0.025*float64(len(completed))
	require.InDelta(t, totalOpus, totalAAC, tol,
		"transcoded AAC total drifts from source Opus beyond priming + boundary quantization")
}

// TestStreamTranscoderDegenerateTimestamps feeds a real WHIP-captured segment
// whose source video carries degenerate timestamps — several frames sharing a
// PTS, plus zero/N/A frame durations (a variable-frame-rate capture artifact
// that ffmpeg tolerates). GStreamer's qtdemux collapses those into buffers with
// no PTS, which made mp4mux abort the whole pipeline ("Could not multiplex
// stream"); the per-stream transcoder then produced NOTHING, so the stream
// silently lost its AAC track for its entire life (a prod incident on a 1080p60
// High-profile Opus stream). Video now drains without entering the muxer, so the segment
// must transcode to a verifiable dual-codec result instead of wedging.
func TestStreamTranscoderDegenerateTimestamps(t *testing.T) {
	ctx := context.Background()
	seg, err := os.ReadFile(remote.RemoteFixture("5b8ddcf569a66d3f8e4a8634d9d422c1848c0becd3c44075afaafdf05f7d731f/h264-opus-degenerate-ts.m4s"))
	require.NoError(t, err)

	ms := newBareSegmentSigner(t)
	keyPEM, err := signers.MarshalES256KPrivateKeyPEM(ms.Signer)
	require.NoError(t, err)
	mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "test.example.com"}}

	var mu sync.Mutex
	var completed [][]byte
	tr := mm.newStreamTranscoder(ctx, "aac", ms.Cert, keyPEM, func(_ any, c []byte, audioOnly bool) {
		mu.Lock()
		appendAudioCompletion(&completed, c, audioOnly)
		mu.Unlock()
	})
	require.NoError(t, tr.Feed(seg, 0), "degenerate-timestamp segment must not wedge the muxer")
	require.NoError(t, tr.Close(),
		"pipeline must drain cleanly, not abort with 'Could not multiplex stream'")

	mu.Lock()
	defer mu.Unlock()
	// Invalid source-video timestamps must not drop the audio completion.
	require.Len(t, completed, 1, "the fed segment must complete to a dual-codec result")

	c := completed[0]
	codecs := audioCodecsOf(t, ctx, c)
	hasOpus, hasAAC := false, false
	for _, cc := range codecs {
		if isOpusCodec(cc) {
			hasOpus = true
		}
		if isAACCodec(cc) {
			hasAAC = true
		}
	}
	require.True(t, hasOpus, "keeps the source Opus track (got %v)", codecs)
	require.True(t, hasAAC, "gains a transcoded AAC track (got %v)", codecs)

	out, err := muxl.RunMuxlVerify(ctx, bytes.NewReader(c))
	require.NoError(t, err)
	require.NotContains(t, out, `"validation_state":"Invalid"`, "completed segment must validate")

	// The transcoded AAC must cover the GoP, not a sliver — the degenerate video
	// timing must not bleed into a truncated audio track.
	o, a := audioDurationsSeconds(t, ctx, c)
	require.Greater(t, a, 0.65*o,
		"transcoded AAC %.3fs far short of source Opus %.3fs", a, o)
}

func TestStreamTranscoderCompletesBeforeNextGoP(t *testing.T) {
	for _, fixture := range []string{"ertmp-red-h264.mp4", "h264-opus-frag.mp4"} {
		t.Run(fixture, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			ms := newBareSegmentSigner(t)
			source := allSignedBareSegments(t, ctx, ms, getFixture(fixture))
			require.GreaterOrEqual(t, len(source), 2)
			codecs := audioCodecsOf(t, ctx, source[0])
			target := "aac"
			for _, codec := range codecs {
				if isAACCodec(codec) {
					target = "opus"
				}
			}
			cert, key := nodeSignerForTest(t)
			mm := &MediaManager{cli: &config.CLI{BroadcasterHost: "test.example.com"}}
			type delivery struct {
				data      []byte
				audioOnly bool
			}
			completed := make(chan delivery, len(source)+1)
			tr := mm.newStreamTranscoder(ctx, target, cert, key, func(_ any, b []byte, audioOnly bool) { completed <- delivery{b, audioOnly} })
			t.Cleanup(func() { require.NoError(t, tr.Close()) })
			require.NoError(t, tr.Feed(source[0], 0))
			var outputs [][]byte
			select {
			case b := <-completed:
				require.False(t, b.audioOnly)
				appendAudioCompletion(&outputs, b.data, b.audioOnly)
			case <-time.After(2 * time.Second):
				t.Fatal("completion waits for the next GoP even though the current source was fully fed")
			}
			for i, b := range source[1:] {
				require.NoError(t, tr.Feed(b, i+1))
			}
			require.NoError(t, tr.Close())
			close(completed)
			for b := range completed {
				appendAudioCompletion(&outputs, b.data, b.audioOnly)
			}
			require.Len(t, outputs, len(source))
			var opusSeconds, aacSeconds float64
			for i, b := range outputs {
				require.True(t, bytes.HasPrefix(b, source[i]), "signed source bytes must remain unchanged")
				_, err := ValidateMP4MediaC2PA(ctx, b)
				require.NoError(t, err)
				opus, aac := audioDurationsSeconds(t, ctx, b)
				require.Greater(t, opus, 0.0)
				require.Greater(t, aac, 0.0)
				opusSeconds += opus
				aacSeconds += aac
			}
			require.InDelta(t, opusSeconds, aacSeconds, .04+.025*float64(len(source)), "continuous encoding must not accumulate priming at each boundary")
		})
	}
}

// appendAudioCompletion assembles delivery chunks for assertions on a complete
// stored source segment, including the independently signed encoder tail.
func appendAudioCompletion(dst *[][]byte, chunk []byte, audioOnly bool) {
	if audioOnly {
		i := len(*dst) - 1
		(*dst)[i] = append((*dst)[i], chunk...)
	} else {
		*dst = append(*dst, chunk)
	}
}

func TestAudioCompletionKeepsLocalAndReplicatedSigningAuthoritySeparate(t *testing.T) {
	for _, callback := range []bool{false, true} {
		t.Run(fmt.Sprint(callback), func(t *testing.T) {
			ctx := t.Context()
			source := audioSignerForTest(t)
			segments := allSignedBareSegments(t, ctx, source, getFixture("ertmp-red-h264.mp4"))
			require.GreaterOrEqual(t, len(segments), 2)
			localSigner, err := source.SigningInput()
			require.NoError(t, err)
			if callback {
				localSigner.KeyPEM = nil
				localSigner.Sign = muxl.SignerToCallback(source.Signer, 32)
			}
			node := newBareSegmentSigner(t)
			nodeInput, err := node.SigningInput()
			require.NoError(t, err)
			mm := NewOffline(&config.CLI{BroadcasterHost: "node.test", AllowedStreams: []string{source.Streamer()}, Syndicate: []string{source.Streamer()}})
			mm.transcoders = map[string]*streamTranscoder{}
			local, _, err := mm.validateSource(ctx, segments[0], true, source.Streamer())
			require.NoError(t, err)
			require.NoError(t, mm.feedStreamTranscoder(ctx, local, segments[0], "opus", localSigner))
			first := mm.transcoders[source.Streamer()]
			remote, _, err := mm.validateSource(ctx, segments[1], false, source.Streamer())
			require.NoError(t, err)
			require.NoError(t, mm.feedStreamTranscoder(ctx, remote, segments[1], "opus", nodeInput))
			require.NotSame(t, first, mm.transcoders[source.Streamer()], "a different signing authority must not inherit the previous encoder")
			require.NoError(t, mm.closeStreamTranscoder(ctx, source.Streamer()))
			buffered := mm.BufferedSegments(source.Streamer())
			require.Len(t, buffered, 2)
			for _, segment := range buffered {
				verified, err := ValidateMP4MediaC2PA(ctx, segment.Data)
				require.NoError(t, err)
				events, err := unwrapMuxlEvents(ctx, segment.Data)
				require.NoError(t, err)
				cat := catalogFromEvents(events)
				for _, audio := range cat.Audio.Renditions {
					if isOpusCodec(audio.Codec) {
						expected := node.DID()
						if segment.Local {
							expected = source.DID()
						}
						require.Equal(t, expected, verified.trackSigningKeys[audio.TrackID()], "replicated media must never borrow a local stream's signing key")
					}
				}
			}
		})
	}
}
