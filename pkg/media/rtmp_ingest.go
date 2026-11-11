package media

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/abema/go-mp4"
	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/codecs"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/av1"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/go-gst/go-gst/pkg/gst"
	app "github.com/go-gst/go-gst/pkg/gstapp"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/constants"
	"stream.place/streamplace/pkg/gstpipeline"
	"stream.place/streamplace/pkg/log"
	"stream.place/streamplace/pkg/muxl"
)

const MaxRTMPVideoTracks = 6

type RTMPVideoTrack struct {
	Track     *gortmplib.Track
	AV1Config []byte
}

type RTMPVideoData struct {
	TrackID int
	AU      [][]byte
	PTS     time.Duration
	DTS     time.Duration
}

type RTMPAACData struct {
	AU  []byte
	PTS time.Duration
}

type RTMPSession struct {
	EventChan   chan any
	VideoTracks []RTMPVideoTrack
	AudioTrack  *gortmplib.Track
	MediaSigner MediaSigner
}

func (mm *MediaManager) RTMPIngest(ctx context.Context, session *RTMPSession) error {
	ctx, err := mm.admitPublisher(ctx, session.MediaSigner.Streamer())
	if err != nil {
		return err
	}
	defer func() {
		if err := mm.closeStreamTranscoder(ctx, session.MediaSigner.Streamer()); err != nil {
			log.Error(ctx, "drain RTMP audio failed", "error", err)
		}
	}()
	signer, err := session.MediaSigner.SigningInput()
	if err != nil {
		return err
	}
	return mm.rtmpIngestSession(ctx, session, func(ctx context.Context, segment []byte) error {
		return mm.receiveSignedSegment(ctx, session.MediaSigner.Streamer(), segment, signer)
	})
}

// rtmpIngestSession muxes each declared video and the original AAC directly
// from RTMP timestamps. Stop native writers before flushing the signer, then
// wait for the final signed GoP to reach the sink.
func (mm *MediaManager) rtmpIngestSession(ctx context.Context, session *RTMPSession, onSegment func(context.Context, []byte) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	signCtx, flush := context.WithCancel(context.WithoutCancel(ctx))
	defer flush()
	var firstErr error
	var once sync.Once
	fail := func(err error) {
		if err != nil {
			once.Do(func() { firstErr = err; cancel() })
		}
	}
	remap, err := rtmpReplicationTrackRemap(session)
	if err != nil {
		return err
	}
	signer, done, err := muxlSignSegmentElem(signCtx, mm.cli, func(ctx context.Context, input io.Reader, events chan *muxl.MuxlEvent) error {
		err := session.MediaSigner.SignSegmentStream(ctx, input, events, remap)
		fail(err)
		return err
	}, len(session.VideoTracks), func(ctx context.Context, segment []byte) error {
		err := onSegment(ctx, segment)
		fail(err)
		return err
	})
	if err != nil {
		return err
	}
	pipeline, sources, err := newRTMPDirectPipeline(session, signer)
	if err != nil {
		flush()
		<-done
		return err
	}
	go mm.HandleKeyRevocation(ctx, session.MediaSigner, pipeline)
	err = runRTMPDirectPipeline(ctx, pipeline, session, sources)
	flush()
	<-done
	if firstErr != nil {
		return firstErr
	}
	return err
}

func newRTMPDirectPipeline(session *RTMPSession, signer gst.Element) (gst.Pipeline, []app.AppSrc, error) {
	if len(session.VideoTracks) == 0 || len(session.VideoTracks) > MaxRTMPVideoTracks {
		return nil, nil, fmt.Errorf("expected 1 to %d video tracks", MaxRTMPVideoTracks)
	}
	if session.AudioTrack == nil {
		return nil, nil, fmt.Errorf("expected one AAC audio track")
	}
	audio, ok := session.AudioTrack.Codec.(*codecs.MPEG4Audio)
	if !ok || audio.Config == nil {
		return nil, nil, fmt.Errorf("expected one AAC audio track")
	}
	audioConfig, err := audio.Config.Marshal()
	if err != nil {
		return nil, nil, fmt.Errorf("AAC configuration: %w", err)
	}
	lines := []string{}
	caps := []string{}
	for i, video := range session.VideoTracks {
		var parser, output string
		switch codec := video.Track.Codec.(type) {
		case *codecs.H264:
			if len(codec.SPS) < 4 || len(codec.SPS) > math.MaxUint16 || len(codec.PPS) == 0 || len(codec.PPS) > math.MaxUint16 {
				return nil, nil, fmt.Errorf("H264 video %d has invalid SPS/PPS", i)
			}
			avcc := &mp4.AVCDecoderConfiguration{
				ConfigurationVersion:       1,
				Profile:                    codec.SPS[1],
				ProfileCompatibility:       codec.SPS[2],
				Level:                      codec.SPS[3],
				Reserved:                   0b111111,
				LengthSizeMinusOne:         3,
				Reserved2:                  0b111,
				NumOfSequenceParameterSets: 1,
				SequenceParameterSets:      []mp4.AVCParameterSet{{Length: uint16(len(codec.SPS)), NALUnit: codec.SPS}},
				NumOfPictureParameterSets:  1,
				PictureParameterSets:       []mp4.AVCParameterSet{{Length: uint16(len(codec.PPS)), NALUnit: codec.PPS}},
			}
			avcc.SetType(mp4.BoxTypeAvcC())
			var config bytes.Buffer
			if _, err := mp4.Marshal(&config, avcc, mp4.Context{}); err != nil {
				return nil, nil, fmt.Errorf("H264 video %d configuration: %w", i, err)
			}
			// Preserve RTMP access-unit boundaries, including SEI after the picture.
			// Annex B reparsing can split that metadata into an untimed extra buffer.
			parser, output = "h264parse", "video/x-h264,stream-format=avc,alignment=au"
			caps = append(caps, fmt.Sprintf("%s,codec_data=(buffer)%x", output, config.Bytes()))
		case *codecs.AV1:
			var config mp4.Av1C
			if _, err := mp4.Unmarshal(bytes.NewReader(video.AV1Config), uint64(len(video.AV1Config)), &config, mp4.Context{}); err != nil || len(config.ConfigOBUs) == 0 {
				return nil, nil, fmt.Errorf("AV1 video %d has no valid sequence-start configuration", i)
			}
			parser, output = "av1parse", "video/x-av1,stream-format=obu-stream,alignment=tu"
			caps = append(caps, fmt.Sprintf("%s,codec_data=(buffer)%x", output, video.AV1Config))
		default:
			return nil, nil, fmt.Errorf("unsupported RTMP video codec: %T", video.Track.Codec)
		}
		lines = append(lines, fmt.Sprintf("appsrc name=source%d format=time block=true max-bytes=%d ! %s ! %s ! capsfilter name=out%d caps=\"%s\"", i, constants.QueueMaxSizeBytes, constants.Queue2Big, parser, i, output))
	}
	audioIndex := len(session.VideoTracks)
	lines = append(lines, fmt.Sprintf("appsrc name=source%d format=time block=true max-bytes=%d ! %s ! aacparse name=out%d", audioIndex, constants.QueueMaxSizeBytes, constants.Queue2Big, audioIndex))
	caps = append(caps, fmt.Sprintf("audio/mpeg,mpegversion=4,stream-format=raw,codec_data=(buffer)%x", audioConfig))
	launched, err := gst.ParseLaunch(strings.Join(lines, "\n"))
	if err != nil {
		return nil, nil, fmt.Errorf("create RTMP ingest pipeline: %w", err)
	}
	pipeline := launched.(gst.Pipeline)
	if !pipeline.Add(signer) {
		return nil, nil, fmt.Errorf("add RTMP signer")
	}
	sources := make([]app.AppSrc, len(caps))
	for i, description := range caps {
		source := pipeline.GetByName(fmt.Sprintf("source%d", i))
		source.SetObjectProperty("caps", gst.CapsFromString(description))
		sources[i] = source.(app.AppSrc)
		padName := fmt.Sprintf("video_%d", i)
		if i == audioIndex {
			padName = "audio_0"
		}
		if ret := pipeline.GetByName(fmt.Sprintf("out%d", i)).GetStaticPad("src").Link(signer.GetStaticPad(padName)); ret != gst.PadLinkOK {
			return nil, nil, fmt.Errorf("link RTMP track %s: %s", padName, ret)
		}
	}
	return pipeline, sources, nil
}

func runRTMPDirectPipeline(ctx context.Context, pipeline gst.Pipeline, session *RTMPSession, sources []app.AppSrc) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		pipeline.SetState(gst.StateNull)
		return fmt.Errorf("start RTMP ingest pipeline")
	}
	feedDone := make(chan error, 1)
	go func() {
		err := feedRTMPPackets(ctx, session, sources)
		if err != nil && ctx.Err() == nil {
			pipeline.PostMessage(gst.NewMessageError(pipeline, err.Error(), err))
		}
		feedDone <- err
	}()
	err := gstpipeline.HandleBusMessages(ctx, pipeline)
	cancel()
	// NULL releases appsrc PushBuffer when a downstream failure stopped it
	// consuming. Join that producer before releasing the pipeline or signer.
	if pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone) == gst.StateChangeFailure {
		return fmt.Errorf("stop RTMP ingest pipeline")
	}
	feedErr := <-feedDone
	if err == nil {
		err = feedErr
	}
	return err
}

// AV1 sync samples need their sequence header in-band, even when it is also
// present in av1C. Cache the configuration so later GoPs can start a decoder
// without changing the encoded frame payload or parsing configuration per frame.
type rtmpAV1Config struct {
	headers                   av1.Bitstream
	reducedStillPictureHeader bool
}

func newRTMPAV1Config(description []byte) (*rtmpAV1Config, error) {
	var config mp4.Av1C
	if _, err := mp4.Unmarshal(bytes.NewReader(description), uint64(len(description)), &config, mp4.Context{}); err != nil {
		return nil, err
	}
	var headers av1.Bitstream
	if err := headers.Unmarshal(config.ConfigOBUs); err != nil {
		return nil, err
	}
	if len(headers) == 0 || av1.OBUType(headers[0][0]>>3) != av1.OBUTypeSequenceHeader {
		return nil, fmt.Errorf("AV1 configuration has no sequence header")
	}
	var sequence av1.SequenceHeader
	if err := sequence.Unmarshal(headers[0]); err != nil {
		return nil, err
	}
	return &rtmpAV1Config{headers: headers, reducedStillPictureHeader: sequence.ReducedStillPictureHeader}, nil
}

func (config *rtmpAV1Config) marshal(units [][]byte, first bool) ([]byte, error) {
	hasHeader, keyframe := false, false
	for _, unit := range units {
		var header av1.OBUHeader
		if err := header.Unmarshal(unit); err != nil {
			return nil, err
		}
		switch header.Type {
		case av1.OBUTypeSequenceHeader:
			hasHeader = true
			if !bytes.Equal(config.headers[0], unit) {
				var sequence av1.SequenceHeader
				if err := sequence.Unmarshal(unit); err != nil {
					return nil, err
				}
				config.headers[0] = bytes.Clone(unit)
				config.reducedStillPictureHeader = sequence.ReducedStillPictureHeader
			}
		case 3, 6: // OBU_FRAME_HEADER, OBU_FRAME.
			payload := unit[1:]
			if header.HasSize {
				var size av1.LEB128
				n, err := size.Unmarshal(payload)
				if err != nil {
					return nil, err
				}
				payload = payload[n:]
				if uint64(len(payload)) != uint64(size) {
					return nil, fmt.Errorf("invalid AV1 frame size")
				}
			}
			if len(payload) == 0 {
				return nil, fmt.Errorf("empty AV1 frame header")
			}
			// In an ordinary frame header, show_existing_frame is followed by the
			// two-bit frame_type. Reduced still-picture headers imply a key frame.
			keyframe = keyframe || config.reducedStillPictureHeader || payload[0]&0xe0 == 0
		}
	}
	if !hasHeader && (first || keyframe) {
		prefixed := make(av1.Bitstream, 0, len(config.headers)+len(units))
		prefixed = append(prefixed, config.headers...)
		units = append(prefixed, units...)
	}
	return av1.Bitstream(units).Marshal()
}

func feedRTMPPackets(ctx context.Context, session *RTMPSession, sources []app.AppSrc) error {
	started := make([]bool, len(session.VideoTracks))
	av1Configs := make([]*rtmpAV1Config, len(session.VideoTracks))
	for id, track := range session.VideoTracks {
		if _, ok := track.Track.Codec.(*codecs.AV1); ok {
			config, err := newRTMPAV1Config(track.AV1Config)
			if err != nil {
				return fmt.Errorf("AV1 track %d configuration: %w", id, err)
			}
			av1Configs[id] = config
		}
	}
	pending := make([][][]byte, len(session.VideoTracks))
	for {
		var source app.AppSrc
		var data []byte
		var pts, dts time.Duration
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-session.EventChan:
			if !ok {
				for _, source := range sources {
					source.EndOfStream()
				}
				return nil
			}
			switch packet := event.(type) {
			case *RTMPVideoData:
				if packet.TrackID < 0 || packet.TrackID >= len(session.VideoTracks) {
					return fmt.Errorf("unknown RTMP video track %d", packet.TrackID)
				}
				track := session.VideoTracks[packet.TrackID]
				units := packet.AU
				switch track.Track.Codec.(type) {
				case *codecs.H264:
					hasPicture := false
					for _, unit := range units {
						if len(unit) > 0 && unit[0]&31 >= 1 && unit[0]&31 <= 5 {
							hasPicture = true
						}
					}
					if !hasPicture {
						if len(pending[packet.TrackID])+len(units) > h264.MaxNALUsPerAccessUnit {
							return fmt.Errorf("H264 pending headers exceed NAL count limit")
						}
						size := 0
						for _, headers := range [][][]byte{pending[packet.TrackID], units} {
							for _, unit := range headers {
								size += len(unit)
								if size > h264.MaxAccessUnitSize {
									return fmt.Errorf("H264 pending headers exceed access unit size limit")
								}
							}
						}
						pending[packet.TrackID] = append(pending[packet.TrackID], units...)
						continue // headers accompany a picture rather than stealing its timestamp
					}
					if len(pending[packet.TrackID]) > 0 {
						units = append(pending[packet.TrackID], units...)
						pending[packet.TrackID] = nil
					}
					var err error
					data, err = h264.AVCC(units).Marshal()
					if err != nil {
						return fmt.Errorf("H264 access unit: %w", err)
					}
				case *codecs.AV1:
					var err error
					data, err = av1Configs[packet.TrackID].marshal(units, !started[packet.TrackID])
					if err != nil {
						return fmt.Errorf("AV1 temporal unit: %w", err)
					}
				}
				started[packet.TrackID] = true
				source, pts, dts = sources[packet.TrackID], packet.PTS, packet.DTS
			case *RTMPAACData:
				source, data, pts, dts = sources[len(session.VideoTracks)], packet.AU, packet.PTS, packet.PTS
			default:
				return fmt.Errorf("unsupported RTMP packet: %T", event)
			}
		}
		if pts < 0 || dts < 0 {
			return fmt.Errorf("negative RTMP timestamp: PTS %s DTS %s", pts, dts)
		}
		buffer, err := gstpipeline.NewBufferFromBytes(data)
		if err != nil {
			return err
		}
		buffer.SetPTS(gst.ClockTime(pts))
		buffer.SetDTS(gst.ClockTime(dts))
		if ret := source.PushBuffer(buffer); ret != gst.FlowOK {
			return fmt.Errorf("push RTMP packet: %s", ret)
		}
	}
}

// RTMPIngestUnpublished runs RTMPIngest's pipeline for the
// --duplicate-mist-test shadow: the same demux, parse, mux and sign path,
// but every signed segment goes to onSegment instead of being validated
// and published, and no streamer state is watched. It returns once the
// stream has ended and the last signed segment has reached onSegment.
func RTMPIngestUnpublished(ctx context.Context, cli *config.CLI, rtmpURL string, ms MediaSigner, onSegment func(ctx context.Context, segment []byte) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Pipeline cancellation must stop GStreamer before closing the signer's
	// input. Closing it while mp4mux is still writing creates a false mux error.
	signCtx, flush := context.WithCancel(context.WithoutCancel(ctx))
	defer flush()
	var firstErr error
	var once sync.Once
	fail := func(err error) {
		if err != nil {
			once.Do(func() { firstErr = err; cancel() })
		}
	}
	signStream := func(c context.Context, input io.Reader, events chan *muxl.MuxlEvent) error {
		err := ms.SignSegmentStream(c, input, events, nil)
		fail(err)
		return err
	}
	sink := func(c context.Context, segment []byte) error {
		err := onSegment(c, segment)
		fail(err)
		return err
	}
	signer, done, err := muxlSignSegmentElem(signCtx, cli, signStream, 1, sink)
	if err != nil {
		return err
	}
	pipeline, err := newRTMPIngestPipeline(rtmpURL, signer)
	if err != nil {
		return err
	}
	err = runRTMPIngestPipeline(ctx, pipeline)
	// GStreamer is now stopped; closing the signer input flushes its final GoP.
	flush()
	<-done
	if firstErr != nil {
		return firstErr
	}
	return err
}

// newRTMPIngestPipeline pulls rtmpURL with rtmp2src and feeds its H.264 and
// AAC tracks into the signer element.
func newRTMPIngestPipeline(rtmpURL string, signer gst.Element) (gst.Pipeline, error) {
	// Mint the source audio: RTMP/FLV audio is already AAC, so pass it through
	// (aacparse) rather than transcoding to Opus. The validate path completes
	// each segment to also carry Opus when a consumer (WebRTC) needs it — so
	// the old RTMP-AAC→Opus→HLS-AAC double-transcode is gone.
	pipelineSlice := []string{
		fmt.Sprintf("rtmp2src location=%s ! flvdemux name=demux", rtmpURL),
		"demux.audio ! queue ! aacparse name=audioenc",
		"demux.video ! queue ! h264parse name=parse",
	}
	pipelineElem, err := gst.ParseLaunch(strings.Join(pipelineSlice, "\n"))
	if err != nil {
		return nil, fmt.Errorf("error creating RTMPIngest pipeline: %w", err)
	}
	pipeline, ok := pipelineElem.(gst.Pipeline)
	if !ok {
		return nil, fmt.Errorf("error creating RTMPIngest pipeline: not a pipeline")
	}

	parseEle := pipeline.GetByName("parse")
	if parseEle == nil {
		return nil, fmt.Errorf("failed to get parse element")
	}

	if !pipeline.Add(signer) {
		return nil, fmt.Errorf("failed to add signer to pipeline")
	}
	if !parseEle.Link(signer) {
		return nil, fmt.Errorf("failed to link parse to signer")
	}
	audioenc := pipeline.GetByName("audioenc")
	if audioenc == nil {
		return nil, fmt.Errorf("failed to get audioenc element")
	}
	if !audioenc.Link(signer) {
		return nil, fmt.Errorf("failed to link audioenc to signer")
	}
	return pipeline, nil
}

// runRTMPIngestPipeline plays pipeline until it ends or ctx does.
func runRTMPIngestPipeline(ctx context.Context, pipeline gst.Pipeline) error {
	busErr := make(chan error, 1)
	go func() {
		busErr <- gstpipeline.HandleBusMessages(ctx, pipeline)
	}()

	if ret := pipeline.SetState(gst.StatePlaying); ret == gst.StateChangeFailure {
		return fmt.Errorf("failed to set pipeline to playing state")
	}

	defer func() {
		if ret := pipeline.SetState(gst.StateNull); ret == gst.StateChangeFailure {
			log.Error(ctx, "error setting pipeline to null state")
		}
	}()

	return <-busErr
}

// rtmpReplicationTrackRemap reserves rendition IDs before the original signer
// hashes the media. Older peers discover only one source video and video IDs
// >=100 as qualities. Keep AV1 and the largest AVC as sources; the other AVC
// tracks use a separate range from node-generated renditions (100 and up).
func rtmpReplicationTrackRemap(session *RTMPSession) (map[uint32]uint32, error) {
	var primary uint32
	var largest uint64
	for i, video := range session.VideoTracks {
		codec, ok := video.Track.Codec.(*codecs.H264)
		if !ok {
			continue
		}
		var sps h264.SPS
		if err := sps.Unmarshal(codec.SPS); err != nil {
			return nil, fmt.Errorf("H264 track %d SPS: %w", i, err)
		}
		area := uint64(sps.Width()) * uint64(sps.Height())
		if primary == 0 || area > largest {
			primary = uint32(i + 1)
			largest = area
		}
	}
	var remap map[uint32]uint32
	for i, video := range session.VideoTracks {
		id := uint32(i + 1)
		if _, ok := video.Track.Codec.(*codecs.H264); ok && id != primary {
			if remap == nil {
				remap = map[uint32]uint32{}
			}
			remap[id] = 1<<16 | id
		}
	}
	return remap, nil
}
