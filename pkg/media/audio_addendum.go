package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/localdb"
	"stream.place/streamplace/pkg/muxl"
)

// ErrAudioSourceMissing lets a receiver retain an addendum that crossed its
// source in transit. It must pass the same checks again after the source arrives.
var ErrAudioSourceMissing = errors.New("audio addendum has no admitted source")

// ReceiveRenditions validates all assets before admitting either video
// renditions or an audio continuation through their existing playback paths.
func (mm *MediaManager) ReceiveRenditions(ctx context.Context, did string, data []byte) error {
	segments, tail, err := verifiedRenditions(ctx, data)
	if err != nil {
		return err
	}
	if tail {
		return mm.receiveAudioAddendum(ctx, did, data, segments)
	}
	return mm.PublishRenditionsForPlayback(context.WithoutCancel(ctx), did, data, true)
}

func verifiedRenditions(ctx context.Context, data []byte) ([]segmentValidation, bool, error) {
	out, err := muxl.RunMuxlVerify(ctx, bytes.NewReader(data))
	if err != nil {
		return nil, false, err
	}
	segments, err := parseVerifiedSegments([]byte(out))
	if err != nil {
		return nil, false, err
	}
	tail := segments[0].Manifest.Title != nil && *segments[0].Manifest.Title == audioTailTitle
	for _, segment := range segments {
		isTail := segment.Manifest.Title != nil && *segment.Manifest.Title == audioTailTitle
		if isTail != tail {
			return nil, false, fmt.Errorf("mixed audio continuation and rendition assets")
		}
	}
	return segments, tail, nil
}

// ReceiveAudioAddendum binds an encoded tail to this stream's already admitted
// source assets. A node signature alone cannot introduce a streamer or change
// the source's visibility, policy, admission generation, or media timeline.
func (mm *MediaManager) ReceiveAudioAddendum(ctx context.Context, did string, data []byte) error {
	if err := mm.CheckPublisherContext(ctx); err != nil {
		return err
	}
	segments, tail, err := verifiedRenditions(ctx, data)
	if err != nil {
		return err
	}
	if !tail {
		return fmt.Errorf("not an audio continuation")
	}
	return mm.receiveAudioAddendum(ctx, did, data, segments)
}

func (mm *MediaManager) receiveAudioAddendum(ctx context.Context, did string, data []byte, segments []segmentValidation) error {
	if err := mm.CheckPublisherContext(ctx); err != nil {
		return err
	}
	restricted, err := audioPublicationRestricted(segments)
	if err != nil {
		return err
	}
	events, err := unwrapMuxlEvents(ctx, data)
	if err != nil {
		return err
	}
	cat := catalogFromEvents(events)
	if cat == nil || cat.Video != nil || cat.Audio == nil || len(cat.Audio.Renditions) != 1 {
		return fmt.Errorf("audio continuation must contain one audio track")
	}
	var id, codec string
	var timescale uint32
	for _, audio := range cat.Audio.Renditions {
		id = strconv.FormatUint(uint64(audio.TrackID()), 10)
		codec, timescale = audio.Codec, audio.Timescale()
	}
	needed := map[string]bool{}
	for _, segment := range segments {
		if strconv.FormatUint(uint64(segment.TrackID), 10) != id || len(segment.Manifest.Ingredients) == 0 {
			return fmt.Errorf("audio continuation has no source ingredients")
		}
		for _, ingredient := range segment.Manifest.Ingredients {
			if ingredient.ActiveManifest == nil {
				return fmt.Errorf("audio continuation has an unsigned ingredient")
			}
			needed[*ingredient.ActiveManifest] = true
		}
	}
	buffered := mm.BufferedSegments(did)
	var matched, contributor *BufferedSegment
	var matchedMeta, policy *SegmentMetadata
	var matchedLabel string
	var matchedAuthority bool
	published := !restricted
	// A codec packet can span adjacent sources. Bind every ingredient to original
	// audio admitted for this streamer, with one policy and admission generation.
	for i := len(buffered) - 1; i >= 0 && i >= len(buffered)-sourceAudioKeep; i-- {
		source := &buffered[i]
		out, err := muxl.RunMuxlVerify(ctx, bytes.NewReader(source.Data))
		if err != nil {
			return err
		}
		assets, err := parseVerifiedSegments([]byte(out))
		if err != nil {
			return err
		}
		primary := &assets[0]
		for j := range assets {
			if assets[j].TrackID < primary.TrackID {
				primary = &assets[j]
			}
		}
		sourceEvents, err := unwrapMuxlEvents(ctx, source.Data)
		if err != nil {
			return err
		}
		sourceCat := catalogFromEvents(sourceEvents)
		if sourceCat == nil || sourceCat.Audio == nil {
			continue
		}
		originalAudio := map[uint32]bool{}
		compatible := false
		for _, audio := range sourceCat.Audio.Renditions {
			originalAudio[audio.TrackID()] = true
			compatible = compatible || strconv.FormatUint(uint64(audio.TrackID()), 10) == id && audio.Codec == codec && audio.Timescale() == timescale
		}
		contributes := false
		for _, asset := range assets {
			if asset.Cert == primary.Cert && originalAudio[asset.TrackID] && asset.Manifest.Label != nil && needed[*asset.Manifest.Label] {
				contributes = true
				delete(needed, *asset.Manifest.Label)
			}
		}
		if !contributes {
			continue
		}
		valid, err := metadataFromVerifiedSegments(ctx, assets)
		if err != nil {
			return err
		}
		if valid.Meta.Creator != did || valid.Manifest.Label == nil {
			return fmt.Errorf("audio source belongs to another streamer")
		}
		if contributor != nil && (source.Local != contributor.Local || source.publisherGeneration != contributor.publisherGeneration) {
			return fmt.Errorf("audio ingredients cross publisher admissions")
		}
		if policy != nil && (!reflect.DeepEqual(policy.ContentWarnings, valid.Meta.ContentWarnings) || !reflect.DeepEqual(policy.ContentRights, valid.Meta.ContentRights) || !reflect.DeepEqual(policy.DistributionPolicy, valid.Meta.DistributionPolicy) || !reflect.DeepEqual(policy.MetadataConfiguration, valid.Meta.MetadataConfiguration)) {
			published = false
		}
		contributor, policy = source, valid.Meta
		published = published && source.Published && valid.Meta.Published
		if matched == nil && compatible {
			var end uint64
			found := false
			for _, event := range sourceEvents {
				if len(event.Tracks[id]) > 0 {
					end = max(end, event.FirstDecodeTimes[id]+event.Durations[id])
					found = true
				}
			}
			for _, event := range events {
				if len(event.Tracks[id]) > 0 {
					if found && event.FirstDecodeTimes[id] == end {
						matched, matchedMeta, matchedLabel = source, valid.Meta, *valid.Manifest.Label
						if !source.Local {
							for _, asset := range assets {
								if strconv.FormatUint(uint64(asset.TrackID), 10) == id && audioCompletionAttests(asset, segments) {
									matchedAuthority = true
									break
								}
							}
						}
					}
					break
				}
			}
		}
		if matched != nil && len(needed) == 0 {
			break
		}
	}
	if matched == nil || len(needed) != 0 && !matchedAuthority {
		return ErrAudioSourceMissing
	}
	meta := *matchedMeta
	meta.Published = published
	vs := &validatedSegment{repoDID: did, label: matchedLabel, meta: &meta, local: matched.Local, publisherGeneration: matched.publisherGeneration}
	return mm.publishAudioAddendum(ctx, vs, data)
}

// A late peer may only have the completed source, whose verified audio already
// binds the same node to the tail's original inputs. This cannot introduce a
// different signer or ingredient, and does not bypass available source policy.
func audioCompletionAttests(completion segmentValidation, tails []segmentValidation) bool {
	labels := make(map[string]bool, len(completion.Manifest.Ingredients))
	for _, ingredient := range completion.Manifest.Ingredients {
		if ingredient.ActiveManifest == nil || labels[*ingredient.ActiveManifest] {
			return false
		}
		labels[*ingredient.ActiveManifest] = true
	}
	if len(labels) == 0 {
		return false
	}
	for _, tail := range tails {
		if tail.Cert != completion.Cert || len(tail.Manifest.Ingredients) == 0 || len(tail.Manifest.Ingredients) > len(labels) {
			return false
		}
		for _, ingredient := range tail.Manifest.Ingredients {
			if ingredient.ActiveManifest == nil || !labels[*ingredient.ActiveManifest] {
				return false
			}
			labels[*ingredient.ActiveManifest] = false
		}
		for label := range labels {
			labels[label] = true
		}
	}
	return true
}

func (mm *MediaManager) publishAudioAddendum(ctx context.Context, vs *validatedSegment, data []byte) error {
	if vs.local {
		var err error
		ctx, err = mm.PublisherContext(ctx, vs.repoDID, vs.publisherGeneration)
		if err != nil {
			return err
		}
	}
	mm.segBuffersMut.Lock()
	defer mm.segBuffersMut.Unlock()
	buffer := mm.segBuffers[vs.repoDID]
	found := false
	if buffer != nil {
		for i := len(buffer.segs) - 1; i >= 0; i-- {
			segment := &buffer.segs[i]
			if segment.sourceLabel == vs.label && segment.Start.Equal(vs.meta.StartTime.Time()) {
				segment.Data = append(bytes.Clone(segment.Data), data...)
				segment.Published = segment.Published && vs.meta.Published
				found = true
				break
			}
		}
	}
	if !found {
		return ErrAudioSourceMissing
	}
	mm.notifySubscribers(ctx, &NewSegmentNotification{
		Segment: &localdb.Segment{ID: vs.label, RepoDID: vs.repoDID, StartTime: vs.meta.StartTime.Time()},
		Muxl:    data, Metadata: vs.meta, Local: vs.local, PublisherGeneration: vs.publisherGeneration, AudioOnly: true,
	})
	return nil
}

// waitSourceHLS waits outside the window lock for its source's asynchronous
// HLS feed. Source and tail notifications already share the buffer's order.
func (mm *MediaManager) waitSourceHLS(ctx context.Context, did, label string) error {
	var ready <-chan struct{}
	mm.segBuffersMut.Lock()
	if buffer := mm.segBuffers[did]; buffer != nil {
		for _, source := range buffer.segs {
			if source.sourceLabel == label {
				ready = source.hlsReady
				break
			}
		}
	}
	mm.segBuffersMut.Unlock()
	if ready == nil {
		return nil
	}
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// PublishAudioAddendum delivers already verified encoded samples after the
// source's publication turn. Opus packets go straight to WebRTC; AAC stays
// canonical for the existing continuous RTMP muxer. Neither path repeats video.
func (mm *MediaManager) PublishAudioAddendum(ctx context.Context, did string, data []byte, source string, start time.Time, published bool) error {
	if err := mm.CheckPublisherContext(ctx); err != nil {
		return err
	}
	if err := mm.waitSourceHLS(ctx, did, source); err != nil {
		return err
	}
	if err := mm.CheckPublisherContext(ctx); err != nil {
		return err
	}
	events, err := unwrapMuxlEvents(ctx, data)
	if err != nil {
		return err
	}
	cat := catalogFromEvents(events)
	if cat == nil || cat.Audio == nil || len(cat.Audio.Renditions) != 1 {
		return fmt.Errorf("audio continuation must contain one audio track")
	}
	seg := &bus.Seg{Filepath: source, Muxl: data, Published: published, AudioOnly: true}
	for _, config := range cat.Audio.Renditions {
		if !isOpusCodec(config.Codec) {
			continue
		}
		id := strconv.FormatUint(uint64(config.TrackID()), 10)
		packet := &bus.PacketizedSegment{}
		for _, event := range events {
			samples := event.Samples[id]
			for i, offset := range samples.Offsets {
				if i >= len(samples.Sizes) || i >= len(samples.Durations) || offset > uint64(len(event.Tracks[id])) || uint64(samples.Sizes[i]) > uint64(len(event.Tracks[id]))-offset || config.Timescale() == 0 {
					return fmt.Errorf("invalid encoded audio sample table")
				}
				duration := time.Duration(samples.Durations[i]) * time.Second / time.Duration(config.Timescale())
				packet.Audio = append(packet.Audio, bus.PacketizedSample{Data: event.Tracks[id][offset : offset+uint64(samples.Sizes[i])], Duration: duration})
				packet.Duration += duration
			}
		}
		seg.PacketizedData = packet
	}
	state := mm.lockLiveWindow(did, true)
	defer state.mu.Unlock()
	if err := mm.CheckPublisherContext(ctx); err != nil {
		return err
	}
	if !published && state.published && !start.Before(state.latest) && state.writer != nil {
		// Captured public playlists retain their writer. Continue privately from
		// its immutable history without copying closed encoded media or losing video.
		state.writer.Flush()
		state.writer = state.writer.Clone()
		state.published = false
	}
	mm.feedLiveWindowEventsLocked(ctx, state, did, events, start, published)
	if err := mm.CheckPublisherContext(ctx); err != nil {
		return err
	}
	if state.writer != nil {
		state.writer.Flush()
	}
	if mm.bus == nil {
		return nil
	}
	if seg.PacketizedData != nil {
		if err := mm.publishRenditionAudioAddendumLocked(ctx, did, source, seg, state); err != nil {
			return err
		}
		mm.bus.PublishSegment(ctx, did, PlaybackSourceRendition, seg)
	}
	mm.bus.PublishSegment(ctx, did, RTMPSourceRendition, seg)
	mm.bus.PublishSegment(ctx, did, "source", seg)
	return nil
}
