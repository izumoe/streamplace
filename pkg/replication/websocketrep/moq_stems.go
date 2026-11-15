package websocketrep

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	official "moq.dev/moq"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/muxl"
)

type moqAudioStem struct {
	AlternateGroup uint32 `json:"alternateGroup"`
	Label          string `json:"label,omitempty"`
}

type moqStemBroadcast struct {
	producer  *official.BroadcastProducer
	tracks    moqTracks
	announced bool
}

// Child audio broadcasts reuse the native CMAF parser and the parent's lifetime.
type moqAudioStems struct {
	origin      *official.OriginProducer
	path        string
	main        uint32
	children    map[uint32]*moqStemBroadcast
	descriptors []moqAudioStem
	batch       time.Duration
}

func (s *moqAudioStems) close() {
	for group, child := range s.children {
		child.tracks.finish()
		_ = child.producer.Close()
		delete(s.children, group)
	}
	s.descriptors = nil
}

func (s *moqAudioStems) configure(cat *muxl.MuxlCatalog, parent moqTracks) error {
	main, selected := media.PlaybackAudioGroups(cat)
	s.main = main
	for name, track := range parent {
		if !track.audio {
			continue
		}
		config, exists := cat.Audio.Renditions[name]
		if !exists || config.AlternateGroup != main {
			_ = track.Finish()
			delete(parent, name)
		}
	}
	descriptors := make([]moqAudioStem, 0, len(selected))
	for _, track := range selected {
		group := track.AlternateGroup
		if s.children[group] == nil {
			producer, err := s.origin.CreateBroadcast(fmt.Sprintf("%s/audio-g%d", s.path, group))
			if err != nil {
				return err
			}
			if s.children == nil {
				s.children = map[uint32]*moqStemBroadcast{}
			}
			s.children[group] = &moqStemBroadcast{producer: producer, tracks: moqTracks{}}
		}
		for name, current := range s.children[group].tracks {
			if config, exists := cat.Audio.Renditions[name]; !exists || config.TrackID() != track.TrackID() {
				_ = current.Finish()
				delete(s.children[group].tracks, name)
			}
		}
		descriptors = append(descriptors, moqAudioStem{AlternateGroup: group, Label: track.Label})
	}
	for group, child := range s.children {
		if !slices.ContainsFunc(descriptors, func(stem moqAudioStem) bool { return stem.AlternateGroup == group }) {
			child.tracks.finish()
			_ = child.producer.Close()
			delete(s.children, group)
		}
	}
	s.descriptors = descriptors
	return nil
}

func (s *moqAudioStems) catalog(parent *official.BroadcastProducer) error {
	ready := make([]moqAudioStem, 0, len(s.descriptors))
	for _, descriptor := range s.descriptors {
		child := s.children[descriptor.AlternateGroup]
		if len(child.tracks) == 0 {
			continue
		}
		if !child.announced {
			if err := child.producer.Announce(official.Route{}); err != nil {
				return err
			}
			child.announced = true
		}
		ready = append(ready, descriptor)
	}
	milliseconds := s.batch / time.Millisecond
	if s.batch%time.Millisecond != 0 {
		milliseconds++
	}
	data, err := json.Marshal(struct {
		BatchDurationMS int64          `json:"batchDurationMs"`
		AudioStems      []moqAudioStem `json:"audioStems,omitempty"`
	}{BatchDurationMS: int64(milliseconds), AudioStems: ready})
	if err != nil {
		return err
	}
	return parent.SetCatalogSection("streamplace", string(data))
}
