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
	"stream.place/streamplace/pkg/localdb"
	"stream.place/streamplace/pkg/log"
	"stream.place/streamplace/pkg/muxl"
)

// Audio codec identification. muxl catalogs carry the MIME-ish codec string
// (`mp4a.40.2` for AAC, `opus` for Opus). We complete every segment to carry
// one of each so downstream pipelines can pick what they need.
func isAACCodec(codec string) bool  { return strings.HasPrefix(codec, "mp4a") }
func isOpusCodec(codec string) bool { return strings.HasPrefix(codec, "opus") }

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
// video track. need is false when both
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
	return missingAudioCodec(haveAAC, haveOpus)
}

// Validation already parsed the source's codecs. Reuse them rather than
// instantiating another WASM module just to inspect its catalog.
func audioCompletionTargetFromMedia(data *localdb.SegmentMediaData) (target string, need bool) {
	if len(data.Video) == 0 {
		return "", false
	}
	var haveAAC, haveOpus bool
	for _, audio := range data.Audio {
		haveAAC = haveAAC || audio.Codec == "aac"
		haveOpus = haveOpus || audio.Codec == "opus"
	}
	return missingAudioCodec(haveAAC, haveOpus)
}

func missingAudioCodec(haveAAC, haveOpus bool) (target string, need bool) {
	switch {
	case haveAAC && !haveOpus:
		return "opus", true
	case haveOpus && !haveAAC:
		return "aac", true
	default:
		return "", false
	}
}

// buildAudioTranscodePipeline continuously encodes audio from fMP4 input.
// Encoded packets go directly to sink; a parallel muxer supplies their native
// initialization through header. Video is drained without entering the muxer.
// Callers install callbacks and start the pipeline. target is "opus" or "aac".
func buildAudioTranscodePipeline(target string) (gst.Pipeline, error) {
	var audioChain string
	switch target {
	case "opus":
		audioChain = "aacparse ! fdkaacdec ! audioconvert ! audioresample ! opusenc name=aenc"
	case "aac":
		audioChain = "opusparse ! opusdec ! audioconvert ! audioresample ! fdkaacenc name=aenc"
	default:
		return nil, fmt.Errorf("unsupported transcode target %q", target)
	}
	element, err := gst.ParseLaunch(strings.Join([]string{
		"appsrc name=src ! qtdemux name=demux",
		"demux.video_0 ! " + constants.Queue2Big + " ! fakesink sync=false async=false",
		"demux.audio_0 ! " + constants.Queue2Big + " name=aq ! " + audioChain + " ! tee name=encoded",
		"encoded. ! appsink name=sink sync=false async=false enable-last-sample=false",
		"encoded. ! mux.audio_0",
		"mp4mux name=mux fragment-duration=1 ! appsink name=header sync=false async=false enable-last-sample=false",
	}, "\n"))
	if err != nil {
		return nil, fmt.Errorf("create transcode pipeline: %w", err)
	}
	pipeline, ok := element.(gst.Pipeline)
	if !ok {
		return nil, fmt.Errorf("create transcode pipeline: parsed description is not a pipeline")
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
	size := 0
	for k, track := range tracks {
		if n, err := strconv.Atoi(k); err == nil {
			ids = append(ids, n)
			size += len(track)
		}
	}
	sort.Ints(ids)
	var out []byte
	if size > 0 {
		out = make([]byte, 0, size)
	}
	for _, id := range ids {
		out = append(out, tracks[strconv.Itoa(id)]...)
	}
	return out
}

// MuxlTracks splits a bare canonical segment into each track's canonical
// segment, bytes and signatures verbatim, keyed by the track's MUXL
// rendition name ("video1", "audio2").
func MuxlTracks(ctx context.Context, seg []byte) (map[string][]byte, error) {
	cat, tracks, _, err := readMuxlTracks(ctx, seg, 0, "")
	if err != nil {
		return nil, err
	}
	return namedMuxlTracks(cat, tracks), nil
}

// H264ReplicationSource selects the largest H264 video for single-video peers,
// retaining original audio and the selected video unchanged. The remaining
// H264 assets travel separately through the peer's rendition-addendum path.
func H264ReplicationSource(ctx context.Context, seg []byte) (source, addendum []byte, err error) {
	_, tracks, renditions, err := readMuxlTracks(ctx, seg, 0, "avc1")
	if err != nil {
		return nil, nil, err
	}
	return concatTracksByID(tracks), concatTracksByID(renditions), nil
}

// H264ReplicationTracks avoids joining and splitting those same signed assets
// when the transport publishes source and rendition tracks separately. ID zero
// picks the largest video; an explicit ID never switches to another source track.
func H264ReplicationTracks(ctx context.Context, seg []byte, videoID uint32) (source, renditions map[string][]byte, err error) {
	cat, tracks, extra, err := readMuxlTracks(ctx, seg, videoID, "avc1")
	if err != nil {
		return nil, nil, err
	}
	return namedMuxlTracks(cat, tracks), namedMuxlTracks(cat, extra), nil
}

func readMuxlTracks(ctx context.Context, seg []byte, videoID uint32, videoCodec string) (*muxl.MuxlCatalog, map[string][]byte, map[string][]byte, error) {
	events, err := unwrapMuxlEvents(ctx, seg)
	if err != nil {
		return nil, nil, nil, err
	}
	cat, tracks := catalogAndTracks(events)
	if cat == nil || len(tracks) == 0 {
		return nil, nil, nil, fmt.Errorf("segment has no MUXL tracks")
	}
	var renditions map[string][]byte
	if videoCodec != "" {
		video, ok := primaryVideoTrack(cat, videoID, videoCodec)
		if !ok {
			return nil, nil, nil, fmt.Errorf("source has no H264 replication track %d", videoID)
		}
		// Codec completion allocates after the source track IDs. Legacy peers
		// must encode their own additional codec: they may not trust our node's
		// audio signer. Keep the single original audio, including Opus inputs.
		if cat.Audio != nil {
			var original uint32
			for _, audio := range cat.Audio.Renditions {
				if original == 0 || audio.TrackID() < original {
					original = audio.TrackID()
				}
			}
			for _, audio := range cat.Audio.Renditions {
				if audio.TrackID() != original {
					delete(tracks, strconv.FormatUint(uint64(audio.TrackID()), 10))
				}
			}
		}
		renditions = map[string][]byte{}
		for _, other := range cat.Video.Renditions {
			if other.TrackID() != video.TrackID() {
				id := strconv.FormatUint(uint64(other.TrackID()), 10)
				if strings.HasPrefix(other.Codec, videoCodec) && other.TrackID() >= renditionTrackBase {
					renditions[id] = tracks[id]
				}
				delete(tracks, id)
			}
		}
	}
	return cat, tracks, renditions, nil
}

func namedMuxlTracks(cat *muxl.MuxlCatalog, tracks map[string][]byte) map[string][]byte {
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
	return out
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
	return selectEventTracks(events, videoID, videoCodec, wantOpus)
}

func selectEventTracks(events []*muxl.MuxlEvent, videoID uint32, videoCodec string, wantOpus bool) ([]byte, error) {
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
