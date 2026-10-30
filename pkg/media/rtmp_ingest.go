package media

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
	ctx = withIngestSession(ctx, mm.nextIngestSession())
	return mm.rtmpIngestSession(ctx, session, func(ctx context.Context, segment []byte) error {
		return mm.receiveSignedSegment(ctx, session.MediaSigner.Streamer(), segment)
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
	signer, done, err := muxlSignSegmentElem(signCtx, mm.cli, func(ctx context.Context, input io.Reader, events chan *muxl.MuxlEvent) error {
		err := session.MediaSigner.SignSegmentStream(ctx, input, events)
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
			if len(codec.SPS) == 0 || len(codec.PPS) == 0 {
				return nil, nil, fmt.Errorf("H264 video %d has no SPS/PPS", i)
			}
			parser, output = "h264parse", "video/x-h264,stream-format=avc,alignment=au"
			caps = append(caps, "video/x-h264,stream-format=byte-stream,alignment=au")
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

func feedRTMPPackets(ctx context.Context, session *RTMPSession, sources []app.AppSrc) error {
	started := make([]bool, len(session.VideoTracks))
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
				switch codec := track.Track.Codec.(type) {
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
					if !started[packet.TrackID] {
						units = append([][]byte{codec.SPS, codec.PPS}, units...)
					}
					var err error
					data, err = h264.AnnexB(units).Marshal()
					if err != nil {
						return fmt.Errorf("H264 access unit: %w", err)
					}
				case *codecs.AV1:
					var err error
					data, err = av1.Bitstream(units).Marshal()
					if err != nil {
						return fmt.Errorf("AV1 temporal unit: %w", err)
					}
					if !started[packet.TrackID] {
						var config mp4.Av1C
						if _, err := mp4.Unmarshal(bytes.NewReader(track.AV1Config), uint64(len(track.AV1Config)), &config, mp4.Context{}); err != nil {
							return err
						}
						data = append(append([]byte(nil), config.ConfigOBUs...), data...)
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
		err := ms.SignSegmentStream(c, input, events)
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
