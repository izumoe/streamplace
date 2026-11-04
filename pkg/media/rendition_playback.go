package media

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/log"
	"stream.place/streamplace/pkg/muxl"
)

// sourceAudioKeep is how many recent source segments' Opus tracks a node
// keeps per streamer, to pair with the rendition addenda that follow them
// a transcoder round trip later.
const sourceAudioKeep = 8

type sourceAudio struct {
	sourceLabel string // admitted source identity, independent of a reconnect's media clock
	tfdt        uint64 // the source video track's first decode time
	timescale   uint32
	needsH264   bool
	opus        []byte // canonical Opus, nil when the source has no compatible audio
}

// rememberSourceAudio keeps the replicated source's identity and video clock,
// plus its Opus track when present, for HLS and WebRTC rendition playback.
func (mm *MediaManager) rememberSourceAudio(did, sourceLabel string, events []*muxl.MuxlEvent) {
	cat, tracks := catalogAndTracks(events)
	if cat == nil || cat.Video == nil || tracks == nil {
		return
	}
	var videoID, opusID string
	video, ok := primaryVideoTrack(cat, 0, "")
	if !ok || video.Timescale() == 0 {
		return
	}
	videoID = strconv.FormatUint(uint64(video.TrackID()), 10)
	_, compatible := primaryVideoTrack(cat, 0, "avc1")
	if cat.Audio != nil {
		for _, a := range cat.Audio.Renditions {
			if isOpusCodec(a.Codec) {
				opusID = strconv.FormatUint(uint64(a.TrackID()), 10)
				break
			}
		}
	}
	if len(tracks[videoID]) == 0 {
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
			list[i].sourceLabel = sourceLabel
			list[i].opus = tracks[opusID]
			list[i].needsH264 = !compatible
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, sourceAudio{sourceLabel: sourceLabel, tfdt: tfdt, timescale: video.Timescale(), needsH264: !compatible, opus: tracks[opusID]})
	}
	if len(list) > sourceAudioKeep {
		drop := len(list) - sourceAudioKeep
		clear(list[:drop])
		list = list[drop:]
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

// PublishRenditionsForPlayback folds verified video renditions into live HLS
// and WebRTC playback. Their decode clocks pair them with the admitted source's
// Opus audio, including an independently received EOS continuation. HLS and
// packetized playback share one publication turn and source visibility.
func (mm *MediaManager) PublishRenditionsForPlayback(ctx context.Context, did string, addendum []byte, published bool) error {
	if err := mm.CheckPublisherContext(ctx); err != nil {
		return err
	}
	if len(addendum) == 0 {
		return nil
	}
	events, err := unwrapMuxlEvents(ctx, addendum)
	if err != nil {
		return fmt.Errorf("read rendition addendum: %w", err)
	}
	cat := catalogFromEvents(events)
	if cat == nil || cat.Video == nil {
		return fmt.Errorf("rendition addendum has no video catalog")
	}
	// The source audio snapshot and publication must take the same turn as an
	// EOS continuation. Otherwise a prepared video can publish older audio after
	// its tail, or miss a tail that arrived while it was being packetized.
	var state *liveWindowState
	var start time.Time
	var audio sourceAudio
	for {
		_, _, source, err := mm.renditionPublication(did, events, published)
		if err != nil {
			return err
		}
		if err := mm.waitSourceHLS(ctx, did, source.sourceLabel); err != nil {
			return err
		}
		state = mm.lockLiveWindow(did, true)
		currentPublished, currentStart, currentSource, err := mm.renditionPublication(did, events, published)
		if err != nil {
			state.mu.Unlock()
			return err
		}
		if source.sourceLabel != currentSource.sourceLabel {
			state.mu.Unlock()
			continue
		}
		published, start, audio = currentPublished, currentStart, currentSource
		break
	}
	defer state.mu.Unlock()
	if err := mm.CheckPublisherContext(ctx); err != nil {
		return err
	}
	if published != state.published && start.Before(state.latest) {
		return nil
	}
	// A queued private continuation can narrow its source before taking its
	// publication turn. Preserve the earlier samples in an isolated writer.
	if !published && state.published && !start.Before(state.latest) {
		if state.writer != nil {
			state.writer.Flush()
			state.writer = state.writer.Clone()
		}
		state.published = false
	}
	mm.feedLiveWindowEventsLocked(ctx, state, did, events, start, published)
	if mm.bus == nil {
		return nil
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
		config := videos[id]
		if len(audio.opus) == 0 {
			continue // HLS can carry video without the Opus-dependent WebRTC path
		}
		name := RenditionName(config.CodedWidth, config.CodedHeight)
		var flat bytes.Buffer
		if err := muxl.RunMuxlWrap(ctx, bytes.NewReader(append(append([]byte{}, video...), audio.opus...)), "flat", &flat); err != nil {
			log.Warn(ctx, "rendition playback: could not present rendition", "streamer", did, "rendition", name, "error", err)
			continue
		}
		seg := &bus.Seg{Filepath: audio.sourceLabel, Data: flat.Bytes(), Published: published}
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
	return nil
}

// renditionPublication captures the admitted audio with its publication decision.
// A late rendition may pair with a continuation that made its source private.
func (mm *MediaManager) renditionPublication(did string, events []*muxl.MuxlEvent, published bool) (bool, time.Time, sourceAudio, error) {
	var start time.Time
	var admitted sourceAudio
	cat := catalogFromEvents(events)
	if cat == nil || cat.Video == nil {
		return false, start, sourceAudio{}, fmt.Errorf("rendition addendum has no video catalog")
	}
	for _, video := range cat.Video.Renditions {
		var track []byte
		for _, event := range events {
			if track = event.Tracks[strconv.FormatUint(uint64(video.TrackID()), 10)]; len(track) > 0 {
				break
			}
		}
		tfdt, ok := firstTfdt(track)
		if !ok {
			return false, start, sourceAudio{}, fmt.Errorf("rendition video has no decode clock")
		}
		audio, ok := mm.sourceAudioAt(did, tfdt, video.Timescale())
		if !ok {
			return false, start, sourceAudio{}, ErrAudioSourceMissing
		}
		if admitted.sourceLabel != "" && admitted.sourceLabel != audio.sourceLabel {
			return false, start, sourceAudio{}, fmt.Errorf("rendition addendum crosses source admissions")
		}
		admitted = audio
		mm.segBuffersMut.Lock()
		found := false
		if buffer := mm.segBuffers[did]; buffer != nil {
			for _, source := range buffer.segs {
				if source.sourceLabel == audio.sourceLabel {
					published = published && source.Published
					if !source.Start.Before(start) {
						start = source.Start
					}
					found = true
					break
				}
			}
		}
		mm.segBuffersMut.Unlock()
		if !found {
			return false, start, sourceAudio{}, ErrAudioSourceMissing
		}
	}
	return published, start, admitted, nil
}

// publishRenditionAudioAddendumLocked either follows an already published
// rendition with its tail, or leaves it in the audio cache for late video.
// Caller holds state.mu across HLS and playback publication.
func (mm *MediaManager) publishRenditionAudioAddendumLocked(ctx context.Context, did, sourceLabel string, playback *bus.Seg, state *liveWindowState) error {
	if err := mm.CheckPublisherContext(ctx); err != nil {
		return err
	}
	var names []string
	if state.writer != nil {
		names = renditionNames(state.writer.VideoTracks())
	}
	if _, local := ctx.Value(publisherAdmissionKey{}).(publisherAdmission); local {
		// The director waits for its video-rendition lane before this call.
		mm.bus.PublishSegment(ctx, did, WebRTCSourceRendition, playback)
		for _, name := range names {
			mm.bus.PublishSegment(ctx, did, name, playback)
		}
		return nil
	}
	mm.sourceAudioMu.Lock()
	found, needsH264 := false, false
	for i := range mm.sourceAudios[did] {
		entry := &mm.sourceAudios[did][i]
		if entry.sourceLabel == sourceLabel {
			combined := make([]byte, 0, len(entry.opus)+len(playback.Muxl))
			combined = append(combined, entry.opus...)
			entry.opus = append(combined, playback.Muxl...)
			found, needsH264 = true, entry.needsH264
			break
		}
	}
	mm.sourceAudioMu.Unlock()
	if !found {
		return fmt.Errorf("audio continuation has no rendition playback source")
	}
	if !needsH264 {
		mm.bus.PublishSegment(ctx, did, WebRTCSourceRendition, playback)
	} else {
		names = append(names, WebRTCSourceRendition)
	}
	for _, name := range names {
		// Reading one cached segment reuses the bus's existing session cache;
		// no second per-rendition completion registry is needed.
		sub := mm.bus.SubscribeSegmentBuf(ctx, did, name, 1)
		var last *bus.Seg
		select {
		case last = <-sub.C:
		default:
		}
		mm.bus.UnsubscribeSegment(context.Background(), did, name, sub)
		if last != nil && !last.AudioOnly && last.Filepath == sourceLabel {
			mm.bus.PublishSegment(ctx, did, name, playback)
		}
	}
	return nil
}
