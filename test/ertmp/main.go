// Publish the browser suite's distinguishable tracks over real Enhanced RTMP.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/abema/go-mp4"
	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/codecs"
	"github.com/bluenviron/gortmplib/pkg/message"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/av1"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	muxl "github.com/streamplace/muxl/go"
)

type sample struct {
	au       [][]byte
	pts, dts time.Duration
}

type fixtureTrack struct {
	track     *gortmplib.Track
	av1Config *mp4.Av1C
	samples   []sample
	duration  time.Duration
	cursor    int
	cycle     time.Duration
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	endpoint, key := os.Getenv("E2E_RTMP_URL"), os.Getenv("E2E_STREAM_KEY")
	if endpoint == "" || key == "" {
		return fmt.Errorf("E2E_RTMP_URL and E2E_STREAM_KEY require the local e2e harness")
	}
	engine, err := muxl.NewWASM(ctx)
	if err != nil {
		return err
	}
	defer engine.Close(context.Background())
	tracks, err := readFixtures(ctx, engine, "test/fixtures")
	if err != nil {
		return err
	}
	address, err := url.Parse(endpoint + "/" + key)
	if err != nil {
		return err
	}
	client := &gortmplib.Client{URL: address, Publish: true}
	if err := client.Initialize(ctx); err != nil {
		return fmt.Errorf("initialize fixture publisher: %w", err)
	}
	defer client.NetConn().Close()
	stop := context.AfterFunc(ctx, func() { _ = client.NetConn().Close() })
	defer stop()
	writer := &gortmplib.Writer{Conn: &fixtureConn{Conn: client, av1Config: tracks[2].av1Config}}
	for _, track := range tracks {
		writer.Tracks = append(writer.Tracks, track.track)
	}
	if err := writer.Initialize(); err != nil {
		return err
	}
	defer func() {
		_ = client.NetConn().Close()
		_ = writer.Wait()
	}()
	started := time.Now()
	for {
		index, packet := nextSample(tracks)
		timer := time.NewTimer(time.Until(started.Add(packet.dts)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if err := client.NetConn().SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return err
		}
		track := tracks[index].track
		switch track.Codec.(type) {
		case *codecs.H264:
			err = writer.WriteH264(track, packet.pts, packet.dts, packet.au)
		case *codecs.AV1:
			err = writer.WriteAV1(track, packet.pts, packet.au)
		case *codecs.MPEG4Audio:
			err = writer.WriteMPEG4Audio(track, packet.pts, packet.au[0])
		}
		if err != nil {
			return err
		}
	}
}

// Each encoded track repeats at its own sample duration; AAC encoder padding
// must not introduce a gap on every video loop.
func nextSample(tracks []*fixtureTrack) (int, sample) {
	index := 0
	for i, track := range tracks {
		selected := tracks[index]
		if track.samples[track.cursor].dts+track.cycle < selected.samples[selected.cursor].dts+selected.cycle {
			index = i
		}
	}
	track := tracks[index]
	packet := track.samples[track.cursor]
	packet.pts += track.cycle
	packet.dts += track.cycle
	track.cursor++
	if track.cursor == len(track.samples) {
		track.cursor = 0
		track.cycle += track.duration
	}
	return index, packet
}

// The pinned writer supplies a placeholder av1C. Replace it with this source's
// real configuration while retaining the library's RTMP serialization.
type fixtureConn struct {
	gortmplib.Conn
	av1Config *mp4.Av1C
}

func (c *fixtureConn) Write(msg message.Message) error {
	start, ok := msg.(*message.VideoExSequenceStart)
	if multi, wrapped := msg.(*message.VideoExMultitrack); wrapped {
		start, ok = multi.Wrapped.(*message.VideoExSequenceStart)
	}
	if ok && start.FourCC == message.FourCCAV1 {
		start.AV1Config = c.av1Config
	}
	return c.Conn.Write(msg)
}

func readFixtures(ctx context.Context, engine muxl.Engine, directory string) ([]*fixtureTrack, error) {
	var tracks []*fixtureTrack
	var audio *fixtureTrack
	for _, name := range []string{"ertmp-red-h264.mp4", "ertmp-blue-h264.mp4", "ertmp-green-av1.mp4"} {
		video, aac, err := readFixture(ctx, engine, filepath.Join(directory, name))
		if err != nil {
			return nil, fmt.Errorf("fixture %s: %w", name, err)
		}
		tracks = append(tracks, video)
		if audio == nil {
			audio = aac
		}
	}
	if audio == nil {
		return nil, fmt.Errorf("fixture has no AAC audio")
	}
	return append(tracks, audio), nil
}

func readFixture(ctx context.Context, engine muxl.Engine, path string) (*fixtureTrack, *fixtureTrack, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	events := make(chan *muxl.Event)
	done := make(chan error, 1)
	go func() {
		done <- engine.SegmentEvents(ctx, bytes.NewReader(data), events)
		close(events)
	}()
	var all []*muxl.Event
	var catalog *muxl.Catalog
	for event := range events {
		all = append(all, event)
		if event.Catalog != nil {
			catalog = event.Catalog
		}
	}
	if err := <-done; err != nil {
		return nil, nil, err
	}
	if catalog == nil || catalog.Video == nil || len(catalog.Video.Renditions) != 1 {
		return nil, nil, fmt.Errorf("expected one video track")
	}
	var videoConfig muxl.VideoConfig
	for _, config := range catalog.Video.Renditions {
		videoConfig = config
	}
	video := &fixtureTrack{}
	var entry, config mp4.BoxType
	switch {
	case strings.HasPrefix(videoConfig.Codec, "avc1."):
		entry, config = mp4.BoxTypeAvc1(), mp4.BoxTypeAvcC()
	case strings.HasPrefix(videoConfig.Codec, "av01."):
		entry, config = mp4.BoxTypeAv01(), mp4.BoxTypeAv1C()
	default:
		return nil, nil, fmt.Errorf("unsupported fixture codec %s", videoConfig.Codec)
	}
	boxes, err := mp4.ExtractBoxWithPayload(bytes.NewReader(data), nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
		mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), entry, config,
	})
	if err != nil {
		return nil, nil, err
	}
	if len(boxes) != 1 {
		return nil, nil, fmt.Errorf("expected one video decoder configuration")
	}
	if config == mp4.BoxTypeAvcC() {
		avcc := boxes[0].Payload.(*mp4.AVCDecoderConfiguration)
		video.track = &gortmplib.Track{Codec: &codecs.H264{SPS: avcc.SequenceParameterSets[0].NALUnit, PPS: avcc.PictureParameterSets[0].NALUnit}}
	} else {
		video.av1Config = boxes[0].Payload.(*mp4.Av1C)
		video.track = &gortmplib.Track{Codec: &codecs.AV1{}}
	}
	var audio *fixtureTrack
	var audioConfig muxl.AudioConfig
	if catalog.Audio != nil {
		for _, config := range catalog.Audio.Renditions {
			if strings.HasPrefix(config.Codec, "mp4a.") {
				audioConfig = config
				boxes, err := mp4.ExtractBoxWithPayload(bytes.NewReader(data), nil, mp4.BoxPath{
					mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
					mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeMp4a(), mp4.BoxTypeEsds(),
				})
				if err != nil {
					return nil, nil, err
				}
				if len(boxes) != 1 {
					return nil, nil, fmt.Errorf("expected one AAC decoder configuration")
				}
				var aac *mpeg4audio.AudioSpecificConfig
				for _, descriptor := range boxes[0].Payload.(*mp4.Esds).Descriptors {
					if descriptor.Tag != mp4.DecSpecificInfoTag {
						continue
					}
					if aac != nil {
						return nil, nil, fmt.Errorf("duplicate AAC decoder-specific information")
					}
					aac = &mpeg4audio.AudioSpecificConfig{}
					if err := aac.Unmarshal(descriptor.Data); err != nil {
						return nil, nil, fmt.Errorf("AAC decoder-specific information: %w", err)
					}
				}
				if aac == nil {
					return nil, nil, fmt.Errorf("missing AAC decoder-specific information")
				}
				audio = &fixtureTrack{track: &gortmplib.Track{Codec: &codecs.MPEG4Audio{Config: aac}}}
			}
		}
	}
	for _, source := range []struct {
		track     *fixtureTrack
		id, scale uint32
	}{{video, videoConfig.TrackID(), videoConfig.Timescale()}, {audio, audioConfig.TrackID(), audioConfig.Timescale()}} {
		if source.track == nil {
			continue
		}
		id := fmt.Sprint(source.id)
		var base uint64
		first := true
		for _, event := range all {
			samples, ok := event.Samples[id]
			if !ok {
				continue
			}
			dts := event.FirstDecodeTimes[id]
			if first {
				base, first = dts, false
			}
			for i, offset := range samples.Offsets {
				encoded := event.Tracks[id][offset : offset+uint64(samples.Sizes[i])]
				packet := sample{
					dts: time.Duration(dts-base) * time.Second / time.Duration(source.scale),
				}
				packet.pts = packet.dts + time.Duration(samples.CtsOffsets[i])*time.Second/time.Duration(source.scale)
				switch source.track.track.Codec.(type) {
				case *codecs.H264:
					var units h264.AVCC
					if err := units.Unmarshal(encoded); err != nil {
						return nil, nil, err
					}
					for _, unit := range units {
						if kind := unit[0] & 31; kind >= 1 && kind <= 5 {
							packet.au = append(packet.au, unit)
						}
					}
				case *codecs.AV1:
					var units av1.Bitstream
					if err := units.Unmarshal(encoded); err != nil {
						return nil, nil, err
					}
					for _, unit := range units {
						if (unit[0]>>3)&15 == 1 {
							sequence, err := av1.Bitstream{unit}.Marshal()
							if err != nil {
								return nil, nil, err
							}
							video.av1Config.ConfigOBUs = sequence
						} else {
							packet.au = append(packet.au, unit)
						}
					}
				case *codecs.MPEG4Audio:
					packet.au = [][]byte{encoded}
				}
				source.track.samples = append(source.track.samples, packet)
				dts += uint64(samples.Durations[i])
				source.track.duration = time.Duration(dts-base) * time.Second / time.Duration(source.scale)
			}
		}
		if len(source.track.samples) == 0 || source.track.duration <= 0 {
			return nil, nil, errors.New("fixture contains no timed samples")
		}
	}
	return video, audio, nil
}
