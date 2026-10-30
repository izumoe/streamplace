package media

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/go-gst/go-gst/pkg/gst"
	"stream.place/streamplace/pkg/atproto"
	"stream.place/streamplace/pkg/constants"
	"stream.place/streamplace/pkg/crypto/signers"
	"stream.place/streamplace/pkg/log"
	"stream.place/streamplace/pkg/muxl"
)

// Audio codec identification. muxl catalogs carry the MIME-ish codec string
// (`mp4a.40.2` for AAC, `opus` for Opus). We complete every segment to carry
// one of each so downstream pipelines can pick what they need.
func isAACCodec(codec string) bool  { return strings.HasPrefix(codec, "mp4a") }
func isOpusCodec(codec string) bool { return strings.HasPrefix(codec, "opus") }

// transcodeManifest is the C2PA manifest for a transcode-completed audio
// track: it opens the source segment as an ingredient and records a
// c2pa.transcoded action referencing it, both by TranscodeIngredientLabel.
// muxl-sign stamps the per-segment time into cawg.metadata as it signs.
func transcodeManifest(creator string) []byte {
	return transcodeManifestFor(creator, "transcoded audio")
}

// transcodeManifestFor is the C2PA manifest a transcoded track is signed
// with: opened + transcoded actions over the source ingredient, and the
// streamer as creator.
func transcodeManifestFor(creator, title string) []byte {
	return []byte(fmt.Sprintf(`{
		"title": %q,
		"assertions": [
			{"label": "c2pa.actions", "data": {"actions": [
				{"action": "c2pa.opened",     "parameters": {"org.cai.ingredientIds": [%q]}},
				{"action": "c2pa.transcoded", "parameters": {"org.cai.ingredientIds": [%q]}}
			]}},
			{"label": "cawg.metadata", "data": {
				"@context": {"dc": "http://purl.org/dc/elements/1.1/"},
				"dc:creator": %q
			}}
		]
	}`, title, muxl.TranscodeIngredientLabel, muxl.TranscodeIngredientLabel, creator))
}

// transcodeSigner returns the node's S2PA cert + PKCS#8 key PEM, built once
// from the server-repo key. The transcode-completed track is signed under the
// node's own did:web identity (it vouches for its transcode), declaring the
// streamer's source track as a parentOf ingredient. The node key is software,
// so it's signed in-wasm via KeyPEM — the same proven path MediaSignerLocal
// uses for ecdsa keys.
func (mm *MediaManager) transcodeSigner() (cert []byte, keyPEM []byte, err error) {
	mm.nodeSignerOnce.Do(func() {
		signer, e := atproto.ServerCryptoSigner()
		if e != nil {
			mm.nodeSignerErr = fmt.Errorf("node transcode signer: %w", e)
			return
		}
		c, e := signers.GenerateES256KCert(signer)
		if e != nil {
			mm.nodeSignerErr = fmt.Errorf("node transcode cert: %w", e)
			return
		}
		k, e := signers.MarshalES256KPrivateKeyPEM(signer)
		if e != nil {
			mm.nodeSignerErr = fmt.Errorf("node transcode key: %w", e)
			return
		}
		mm.nodeCert = c
		mm.nodeKeyPEM = k
	})
	return mm.nodeCert, mm.nodeKeyPEM, mm.nodeSignerErr
}

// audioCompletionTarget inspects a segment's catalog and reports the codec to
// add ("opus" or "aac") when exactly one of AAC/Opus is present alongside a
// video track (needed as the segmentation clock). need is false when both
// codecs are already present, there's no audio, there's no video, or the codec
// is neither — in which case the segment is distributed as-is.
func (mm *MediaManager) audioCompletionTarget(ctx context.Context, seg []byte) (target string, need bool) {
	events, err := unwrapMuxlEvents(ctx, seg)
	if err != nil {
		log.Warn(ctx, "codec-completion inspect: unwrap failed", "error", err)
		return "", false
	}
	cat := catalogFromEvents(events)
	if cat == nil || cat.Audio == nil || cat.Video == nil {
		return "", false
	}
	var haveAAC, haveOpus bool
	for _, a := range cat.Audio.Renditions {
		if isAACCodec(a.Codec) {
			haveAAC = true
		}
		if isOpusCodec(a.Codec) {
			haveOpus = true
		}
	}
	switch {
	case haveAAC && !haveOpus:
		return "opus", true
	case haveOpus && !haveAAC:
		return "aac", true
	default:
		return "", false
	}
}

// buildAudioTranscodePipeline constructs the audio-transcode pipeline shared by
// the per-segment ([transcodeAudioSegment]) and continuous ([streamTranscoder])
// paths: an `appsrc` of fMP4 → qtdemux → encoded video passthrough + audio
// (decode → re-encode to target) → fragmented mp4mux → `appsink`. Video rides
// along only so muxl can anchor the canonical segment on the video keyframe;
// callers keep the original signed video and use only the transcoded audio
// track. Callers wire the `src`/`sink` callbacks and set the pipeline playing.
// target is the codec being produced: "opus" (source AAC) or "aac" (source Opus).
func buildAudioTranscodePipeline(target string) (gst.Pipeline, error) {
	// Queue sizing: a single qtdemux feeds both branches, so if either queue
	// hits a limit and blocks the demux, the sibling branch starves and mp4mux
	// deadlocks waiting for it (then appsrc backpressures and the Feed blocks).
	// gst's default queue caps at max-size-time=1s, which a long GoP overflows:
	// a 2s GoP overflowed the video queue's time cap and wedged a live stream
	// (TestStreamTranscoderDoubleGopWedge). Use the shared Queue2Big preset
	// (no time/buffer cap, generous byte cap) like the other demux-fed pipelines
	// (rtmp_push, packetize, media_data_parser) so an over-long GoP flows
	// through instead of deadlocking, while memory stays bounded.
	var audioChain string
	switch target {
	case "opus": // source is AAC
		audioChain = constants.Queue2Big + " name=aq ! aacparse ! fdkaacdec ! audioconvert ! audioresample ! opusenc name=aenc"
	case "aac": // source is Opus
		audioChain = constants.Queue2Big + " name=aq ! opusparse ! opusdec ! audioconvert ! audioresample ! fdkaacenc name=aenc"
	default:
		return nil, fmt.Errorf("unsupported transcode target %q", target)
	}

	launched, err := gst.ParseLaunch(strings.Join([]string{
		"appsrc name=src ! qtdemux name=demux",
		"demux.video_0 ! " + constants.Queue2Big + " name=vq ! parsebin name=vparse ! identity name=vout",
		"demux.audio_0 ! " + audioChain,
	}, "\n"))
	if err != nil {
		return nil, fmt.Errorf("create transcode pipeline: %w", err)
	}
	pipeline, ok := launched.(gst.Pipeline)
	if !ok {
		return nil, fmt.Errorf("create transcode pipeline: parsed description is not a pipeline")
	}

	// Fragmented muxer + appsink, built programmatically so we can set the
	// enum fragment-mode and pre-request pads in video-then-audio order.
	mux := gst.ElementFactoryMakeWithProperties("mp4mux", map[string]any{
		"name":              "mux",
		"fragment-mode":     int32(0),
		"fragment-duration": uint32(1),
	})
	if mux == nil {
		return nil, fmt.Errorf("create mp4mux: element factory returned nil")
	}
	sink := gst.ElementFactoryMakeWithProperties("appsink", map[string]any{"name": "sink", "sync": false})
	if sink == nil {
		return nil, fmt.Errorf("create appsink: element factory returned nil")
	}
	if !pipeline.AddMany(mux, sink) {
		return nil, fmt.Errorf("add mux/sink to pipeline failed")
	}
	if !mux.Link(sink) {
		return nil, fmt.Errorf("link mux→sink failed")
	}

	// Pre-request pads in order so the muxer assigns video=track 1, audio=2.
	videoMuxPad := mux.RequestPadSimple("video_%u")
	audioMuxPad := mux.RequestPadSimple("audio_%u")
	if videoMuxPad == nil || audioMuxPad == nil {
		return nil, fmt.Errorf("failed to request mp4mux pads")
	}

	vparse := pipeline.GetByName("vparse")
	if vparse == nil {
		return nil, fmt.Errorf("vparse element not found")
	}
	// Only H264 needs the IDR correction. AV1's parser supplies its own sync
	// flags; interpreting its OBUs as H264 NALs would erase every cut point.
	vparse.ConnectPadAdded(func(_ gst.Element, pad gst.Pad) {
		if caps := padCaps(pad); caps != nil && caps.GetStructure(0).GetName() == "video/x-h264" {
			installIDRKeyframeProbe(context.Background(), pad, "completion")
		}
	})
	aenc := pipeline.GetByName("aenc")
	if aenc == nil {
		return nil, fmt.Errorf("aenc element not found")
	}
	// Repair the passthrough video's timestamps before the muxer. Variable-frame-
	// rate WHIP capture can emit several frames at one instant (observed: three
	// frames sharing a PTS, sub-100µs "durations", N/A durations — a capture
	// artifact ffmpeg tolerates). GStreamer's qtdemux collapses those into buffers
	// with a zero duration and then no PTS *and* no DTS; mp4mux rejects a PTS-less
	// buffer ("Buffer has no PTS" → "Could not multiplex stream"), which kills the
	// whole pipeline — so the stream silently loses its transcoded AAC track for
	// the rest of its life (observed in prod on a 1080p60 High-profile Opus
	// stream). The muxed video is only a segmentation clock and is discarded
	// downstream (finishTranscodedSegment keeps just the transcoded audio), so we
	// force a valid, strictly increasing PTS: keep the real one where present,
	// otherwise carry the previous timestamp forward by one step. Keyframes (the
	// GoP/segment cut points) always carry a real PTS, so segment boundaries — and
	// the 1:1 source pairing — are preserved; only degenerate intra-GoP frames are
	// nudged, by at most a few ms within their own GoP.
	const tsStep = gst.ClockTime(1_000_000) // 1 ms — ≥1 tick at any sane video timescale
	var lastTS gst.ClockTime
	haveLast := false
	vparseSrc := pipeline.GetByName("vout").GetStaticPad("src")
	vparseSrc.AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buf := info.GetBuffer()
		if buf == nil {
			return gst.PadProbeOK
		}
		ts := buf.PTS()
		if ts == gst.ClockTimeNone {
			ts = buf.DTS()
		}
		if haveLast && (ts == gst.ClockTimeNone || ts <= lastTS) {
			ts = lastTS + tsStep // missing or non-monotonic → carry forward
		} else if ts == gst.ClockTimeNone {
			ts = 0 // very first buffer with no timestamp at all
		}
		buf.SetPTS(ts)
		if d := buf.Duration(); d == gst.ClockTimeNone || d == 0 {
			buf.SetDuration(tsStep)
		}
		lastTS = ts
		haveLast = true
		return gst.PadProbeOK
	})
	if r := vparseSrc.Link(videoMuxPad); r != gst.PadLinkOK {
		return nil, fmt.Errorf("link video chain → mux: %v", r)
	}
	if r := aenc.GetStaticPad("src").Link(audioMuxPad); r != gst.PadLinkOK {
		return nil, fmt.Errorf("link audio chain → mux: %v", r)
	}

	return pipeline, nil
}

// --- muxl event helpers ---

func unwrapMuxlEvents(ctx context.Context, seg []byte) ([]*muxl.MuxlEvent, error) {
	return collectMuxlEvents(func(ch chan *muxl.MuxlEvent) error {
		return muxl.RunMuxlUnwrapEvents(ctx, bytes.NewReader(seg), ch)
	})
}

func segmentMuxlEvents(ctx context.Context, fmp4 []byte) ([]*muxl.MuxlEvent, error) {
	return collectMuxlEvents(func(ch chan *muxl.MuxlEvent) error {
		return muxl.RunMuxlSegmenterEvents(ctx, bytes.NewReader(fmp4), ch)
	})
}

func collectMuxlEvents(run func(chan *muxl.MuxlEvent) error) ([]*muxl.MuxlEvent, error) {
	ch := make(chan *muxl.MuxlEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		err := run(ch)
		close(ch)
		errCh <- err
	}()
	var out []*muxl.MuxlEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out, <-errCh
}

func catalogFromEvents(events []*muxl.MuxlEvent) *muxl.MuxlCatalog {
	var cat *muxl.MuxlCatalog
	for _, ev := range events {
		if ev.Type == "init" && ev.Catalog != nil {
			cat = ev.Catalog
		}
	}
	return cat
}

// catalogAndTracks returns the catalog and every track's signed fragments in
// event order. Tracks appearing once retain their original byte slice.
func catalogAndTracks(events []*muxl.MuxlEvent) (*muxl.MuxlCatalog, map[string][]byte) {
	var tracks map[string][]byte
	for _, ev := range events {
		if ev.Type != "segment" && ev.Type != "signed-segment" {
			continue
		}
		for id := range ev.Tracks {
			if tracks == nil {
				tracks = make(map[string][]byte, len(ev.Tracks))
			}
			if _, found := tracks[id]; !found {
				tracks[id] = concatTrackAcrossSegments(events, id)
			}
		}
	}
	return catalogFromEvents(events), tracks
}

func maxU32(a, b uint32) uint32 {
	if a > b {
		return a
	}
	return b
}

// concatTracksByID reassembles a bare canonical .m4s from a segment event's
// per-track bytes in ascending track-id order (the canonical track order) — the
// inverse of how catalogAndTracks/the segmenter split a segment into ev.Tracks.
func concatTracksByID(tracks map[string][]byte) []byte {
	ids := make([]int, 0, len(tracks))
	for k := range tracks {
		if n, err := strconv.Atoi(k); err == nil {
			ids = append(ids, n)
		}
	}
	sort.Ints(ids)
	var out []byte
	for _, id := range ids {
		out = append(out, tracks[strconv.Itoa(id)]...)
	}
	return out
}

// MuxlTracks splits a bare canonical segment into each track's canonical
// segment, bytes and signatures verbatim, keyed by the track's MUXL
// rendition name ("video1", "audio2").
func MuxlTracks(ctx context.Context, seg []byte) (map[string][]byte, error) {
	events, err := unwrapMuxlEvents(ctx, seg)
	if err != nil {
		return nil, err
	}
	cat, tracks := catalogAndTracks(events)
	if cat == nil || len(tracks) == 0 {
		return nil, fmt.Errorf("segment has no MUXL tracks")
	}
	out := map[string][]byte{}
	add := func(name string, id uint32) {
		if b := tracks[strconv.FormatUint(uint64(id), 10)]; len(b) > 0 {
			out[name] = b
		}
	}
	if cat.Video != nil {
		for name, v := range cat.Video.Renditions {
			add(name, v.TrackID())
		}
	}
	if cat.Audio != nil {
		for name, a := range cat.Audio.Renditions {
			add(name, a.TrackID())
		}
	}
	return out, nil
}

// concatTrackAcrossSegments returns a track's fragments in event order without
// changing their bytes. A track appearing once is returned without a copy.
func concatTrackAcrossSegments(events []*muxl.MuxlEvent, key string) []byte {
	var first []byte
	size := 0
	for _, ev := range events {
		if ev.Type == "segment" || ev.Type == "signed-segment" {
			track := ev.Tracks[key]
			if len(first) == 0 {
				first = track
			}
			size += len(track)
		}
	}
	if size == len(first) {
		return first
	}
	out := make([]byte, 0, size)
	for _, ev := range events {
		if ev.Type == "segment" || ev.Type == "signed-segment" {
			out = append(out, ev.Tracks[key]...)
		}
	}
	return out
}

// primaryVideoTrack selects the largest compatible picture, with a stable ID
// tie-break. An explicit ID never falls back to another track.
func primaryVideoTrack(cat *muxl.MuxlCatalog, id uint32, codec string) (muxl.MuxlVideoConfig, bool) {
	var best muxl.MuxlVideoConfig
	found := false
	if cat == nil || cat.Video == nil {
		return best, false
	}
	for _, video := range cat.Video.Renditions {
		if id != 0 && video.TrackID() != id || !strings.HasPrefix(video.Codec, codec) {
			continue
		}
		area := uint64(video.CodedWidth) * uint64(video.CodedHeight)
		bestArea := uint64(best.CodedWidth) * uint64(best.CodedHeight)
		if !found || area > bestArea || area == bestArea && (video.CodedWidth > best.CodedWidth || video.CodedWidth == best.CodedWidth && video.TrackID() < best.TrackID()) {
			best, found = video, true
		}
	}
	return best, found
}

// selectSegmentTracks preserves one video and its requested audio codec,
// carrying every fragment's signed bytes unchanged. ID zero selects primary.
func selectSegmentTracks(ctx context.Context, seg []byte, videoID uint32, videoCodec string, wantOpus bool) ([]byte, error) {
	events, err := unwrapMuxlEvents(ctx, seg)
	if err != nil {
		return nil, fmt.Errorf("unwrap segment for track selection: %w", err)
	}
	cat := catalogFromEvents(events)
	keep := map[uint32]bool{}
	if cat != nil && cat.Video != nil && len(cat.Video.Renditions) > 0 {
		video, ok := primaryVideoTrack(cat, videoID, videoCodec)
		if !ok {
			return nil, fmt.Errorf("no compatible video track %d (%s)", videoID, videoCodec)
		}
		keep[video.TrackID()] = true
	} else if videoID != 0 {
		return nil, fmt.Errorf("video track %d missing", videoID)
	}
	if cat != nil && cat.Audio != nil && len(cat.Audio.Renditions) > 0 {
		var chosen uint32
		found := false
		for _, audio := range cat.Audio.Renditions {
			if (wantOpus && isOpusCodec(audio.Codec)) || (!wantOpus && isAACCodec(audio.Codec)) {
				if !found || audio.TrackID() < chosen {
					chosen, found = audio.TrackID(), true
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("requested audio codec unavailable (opus=%t)", wantOpus)
		}
		keep[chosen] = true
	}
	ids := make([]uint32, 0, len(keep))
	for id := range keep {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var out []byte
	for _, event := range events {
		if event.Type != "segment" && event.Type != "signed-segment" {
			continue
		}
		for _, id := range ids {
			out = append(out, event.Tracks[strconv.FormatUint(uint64(id), 10)]...)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("segment has no selected tracks")
	}
	return out, nil
}
