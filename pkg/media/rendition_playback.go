package media

import (
	"bytes"
	"context"
	"math"
	"sort"
	"strconv"

	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/log"
	"stream.place/streamplace/pkg/muxl"
)

// sourceAudioKeep is how many recent source segments' Opus tracks a node
// keeps per streamer, to pair with the rendition addenda that follow them
// a transcoder round trip later.
const sourceAudioKeep = 8

type sourceAudio struct {
	tfdt      uint64 // the source video track's first decode time
	timescale uint32
	needsH264 bool
	opus      []byte // the Opus track's canonical bytes
}

// rememberSourceAudio keeps a replicated source segment's Opus track, keyed
// by its video track's first decode time, for PublishRenditionsForPlayback.
func (mm *MediaManager) rememberSourceAudio(ctx context.Context, did string, seg []byte) {
	events, err := unwrapMuxlEvents(ctx, seg)
	if err != nil {
		log.Debug(ctx, "rendition playback: could not read source segment", "streamer", did, "error", err)
		return
	}
	cat, tracks := catalogAndTracks(events)
	if cat == nil || cat.Video == nil || cat.Audio == nil || tracks == nil {
		return
	}
	var videoID, opusID string
	video, ok := primaryVideoTrack(cat, 0, "")
	if !ok || video.Timescale() == 0 {
		return
	}
	videoID = strconv.FormatUint(uint64(video.TrackID()), 10)
	_, compatible := primaryVideoTrack(cat, 0, "avc1")
	for _, a := range cat.Audio.Renditions {
		if isOpusCodec(a.Codec) {
			opusID = strconv.FormatUint(uint64(a.TrackID()), 10)
			break
		}
	}
	if videoID == "" || opusID == "" || len(tracks[opusID]) == 0 {
		return
	}
	tfdt, ok := firstTfdt(tracks[videoID])
	if !ok {
		return
	}
	mm.sourceAudioMu.Lock()
	defer mm.sourceAudioMu.Unlock()
	if mm.sourceAudios == nil {
		mm.sourceAudios = map[string][]sourceAudio{}
	}
	list := mm.sourceAudios[did]
	// A publisher reconnect starts a fresh media timeline, so early tfdt
	// values repeat while the previous session's entries are still cached:
	// the newest audio at a tfdt is the one that goes with the renditions
	// arriving now, so it replaces an older entry rather than sitting
	// behind it.
	replaced := false
	for i := range list {
		if list[i].tfdt == tfdt && list[i].timescale == video.Timescale() {
			list[i].opus = tracks[opusID]
			list[i].needsH264 = !compatible
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, sourceAudio{tfdt: tfdt, timescale: video.Timescale(), needsH264: !compatible, opus: tracks[opusID]})
	}
	if len(list) > sourceAudioKeep {
		list = list[len(list)-sourceAudioKeep:]
	}
	mm.sourceAudios[did] = list
}

func (mm *MediaManager) sourceAudioAt(did string, tfdt uint64, timescale uint32) (sourceAudio, bool) {
	mm.sourceAudioMu.Lock()
	defer mm.sourceAudioMu.Unlock()
	list := mm.sourceAudios[did]
	for i := len(list) - 1; i >= 0; i-- {
		// Retime rounds to the rendition's output tick.
		if timescale != 0 && math.Abs(float64(list[i].tfdt)/float64(list[i].timescale)-float64(tfdt)/float64(timescale)) <= 1/float64(timescale) {
			return list[i], true
		}
	}
	return sourceAudio{}, false
}

// PublishRenditionsForPlayback makes an addendum of rendition tracks (see
// MintVideoRenditions) playable over WebRTC on this node: each rendition,
// paired with the Opus audio of the source segment it was transcoded from
// (the renditions are re-timed onto the source video's decode time, which
// is the pairing key), is presented, packetized and published on the bus
// under the rendition's name ("720p"), as the transcoding node publishes
// its own. A node that only syndicates the stream received the renditions
// for HLS but had nothing on its bus, so a WebRTC viewer who picked one
// got no video.
func (mm *MediaManager) PublishRenditionsForPlayback(ctx context.Context, did string, addendum []byte, published bool) {
	if mm.bus == nil || len(addendum) == 0 {
		return
	}
	events, err := unwrapMuxlEvents(ctx, addendum)
	if err != nil {
		log.Warn(ctx, "rendition playback: could not read addendum", "streamer", did, "error", err)
		return
	}
	cat := catalogFromEvents(events)
	if cat == nil || cat.Video == nil {
		return
	}
	videos := map[string]muxl.MuxlVideoConfig{}
	for _, v := range cat.Video.Renditions {
		if v.CodedHeight > 0 {
			videos[strconv.FormatUint(uint64(v.TrackID()), 10)] = v
		}
	}
	primary, _ := primaryVideoTrack(cat, 0, "avc1")
	ids := make([]string, 0, len(videos))
	for id := range videos {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		video := concatTrackAcrossSegments(events, id)
		tfdt, ok := firstTfdt(video)
		if !ok {
			continue
		}
		config := videos[id]
		audio, found := mm.sourceAudioAt(did, tfdt, config.Timescale())
		if !found {
			log.Debug(ctx, "rendition playback: no source audio for this rendition segment", "streamer", did, "tfdt", tfdt)
			continue
		}
		name := RenditionName(config.CodedWidth, config.CodedHeight)
		var flat bytes.Buffer
		if err := muxl.RunMuxlWrap(ctx, bytes.NewReader(append(append([]byte{}, video...), audio.opus...)), "flat", &flat); err != nil {
			log.Warn(ctx, "rendition playback: could not present rendition", "streamer", did, "rendition", name, "error", err)
			continue
		}
		seg := &bus.Seg{Data: flat.Bytes(), Published: published}
		packet, err := Packetize(ctx, mm.cli, seg, 0)
		if err != nil {
			log.Warn(ctx, "rendition playback: could not packetize rendition", "streamer", did, "rendition", name, "error", err)
			continue
		}
		seg.PacketizedData = packet
		mm.bus.PublishSegment(ctx, did, name, seg)
		if audio.needsH264 && id == strconv.FormatUint(uint64(primary.TrackID()), 10) {
			mm.bus.PublishSegment(ctx, did, WebRTCSourceRendition, seg)
		}
	}
}
