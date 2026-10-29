package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	app "github.com/go-gst/go-gst/pkg/gstapp"
	"go.opentelemetry.io/otel"
	"stream.place/streamplace/pkg/constants"
	"stream.place/streamplace/pkg/gstpipeline"
	"stream.place/streamplace/pkg/localdb"
	"stream.place/streamplace/pkg/log"
)

func ParseSegmentMediaData(ctx context.Context, mp4bs []byte) (*localdb.SegmentMediaData, error) {
	ctx, span := otel.Tracer("signer").Start(ctx, "ParseSegmentMediaData")
	defer span.End()
	ctx = log.WithLogValues(ctx, "GStreamerFunc", "ParseSegmentMediaData")
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	launched, err := gst.ParseLaunch("appsrc name=appsrc ! qtdemux name=demux")
	if err != nil {
		return nil, fmt.Errorf("error creating SegmentMetadata pipeline: %w", err)
	}
	pipeline, ok := launched.(gst.Pipeline)
	if !ok {
		return nil, fmt.Errorf("error creating SegmentMetadata pipeline: parsed description is not a pipeline")
	}
	appsrc := pipeline.GetByName("appsrc")
	if appsrc == nil {
		return nil, fmt.Errorf("error creating SegmentMetadata pipeline: appsrc not found")
	}
	appsrc.(app.AppSrc).SetCallbacks(&app.AppSrcCallbacks{
		NeedData: gstpipeline.ReaderNeedDataIncremental(ctx, bytes.NewReader(mp4bs)),
	})
	type parsedTrack struct {
		id       uint32
		video    *localdb.SegmentMediadataVideo
		audio    *localdb.SegmentMediadataAudio
		found    bool
		duration time.Duration
		span     sampleSpan
	}
	var tracks []*parsedTrack
	var padsAdded atomic.Int32
	padProbe := func(pad gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if info.GetEvent().GetType() != gst.EventEOS {
			return gst.PadProbeOK
		}
		if count := padsAdded.Load(); count < 2 {
			err := fmt.Errorf("expected at least 2 tracks (video + audio), got %d", count)
			if parent := pad.GetParentElement(); parent != nil {
				parent.PostMessage(gst.NewMessageError(parent, err.Error(), err))
			} else {
				cancel()
			}
		}
		return gst.PadProbeRemove
	}
	parseErr := make(chan error, 1)
	fail := func(err error) {
		select {
		case parseErr <- err:
		default:
		}
		cancel()
	}
	demux := pipeline.GetByName("demux")
	if demux == nil {
		return nil, fmt.Errorf("error creating SegmentMetadata pipeline: demux not found")
	}
	handler := demux.ConnectPadAdded(func(_ gst.Element, pad gst.Pad) {
		padsAdded.Add(1)
		pad.AddProbe(gst.PadProbeTypeEventDownstream, padProbe)
		caps := pad.GetCurrentCaps()
		if caps == nil || caps.GetStructure(0) == nil {
			fail(fmt.Errorf("unable to get track caps"))
			return
		}
		structure := caps.GetStructure(0)
		name := structure.GetName()
		// qtdemux appends the MP4 track ID to the stream ID. Pad names are
		// ordinals (video_0, video_1), which must not become signed track IDs.
		streamID := pad.GetStreamID()
		id, err := strconv.ParseUint(streamID[strings.LastIndexByte(streamID, '/')+1:], 10, 32)
		if err != nil || id == 0 {
			fail(fmt.Errorf("invalid MP4 track stream ID %q", streamID))
			return
		}
		track := &parsedTrack{id: uint32(id)}
		switch name {
		case "video/x-h264", "video/x-av1":
			codec := "h264"
			if name == "video/x-av1" {
				codec = "av1"
			}
			track.video = &localdb.SegmentMediadataVideo{TrackID: track.id, Codec: codec}
			if width, ok := structure.GetInt("width"); ok {
				track.video.Width = int(width)
			}
			if height, ok := structure.GetInt("height"); ok {
				track.video.Height = int(height)
			}
			if num, den, ok := structure.GetFraction("framerate"); ok && num != 0 && den != 0 {
				track.video.FPSNum, track.video.FPSDen = int(num), int(den)
			}
		case "audio/x-opus", "audio/mpeg":
			codec := "opus"
			if name == "audio/mpeg" {
				version, _ := structure.GetInt("mpegversion")
				if version != 2 && version != 4 {
					fail(fmt.Errorf("unsupported audio codec %q", caps.String()))
					return
				}
				codec = "aac"
			}
			track.audio = &localdb.SegmentMediadataAudio{TrackID: track.id, Codec: codec}
			if rate, ok := structure.GetInt("rate"); ok {
				track.audio.Rate = int(rate)
			}
			if channels, ok := structure.GetInt("channels"); ok {
				track.audio.Channels = int(channels)
			}
		default:
			fail(fmt.Errorf("unsupported media codec %q", name))
			return
		}
		description := constants.Queue2Big + " ! appsink sync=false name=samples"
		if name == "video/x-h264" {
			description = fmt.Sprintf("%s ! tee name=t t. ! %s ! appsink sync=false name=samples t. ! %s ! h2642json ! appsink sync=false name=json", constants.Queue2Big, constants.Queue2Big, constants.Queue2Big)
		}
		bin, err := gst.ParseBinFromDescription(description, true)
		if err != nil || !pipeline.Add(bin) {
			fail(fmt.Errorf("create metadata track bin: %v", err))
			return
		}
		bin.GetByName("samples").(app.AppSink).SetCallbacks(&app.AppSinkCallbacks{
			NewSample: ParseSegmentMediaDataSinkNewSampleFunc(ctx, &track.found, &track.duration, &track.span),
		})
		if name == "video/x-h264" {
			bin.GetByName("json").(app.AppSink).SetCallbacks(&app.AppSinkCallbacks{
				NewSample: func(sink app.AppSink) gst.FlowReturn {
					sample := sink.PullSample()
					if sample == nil {
						return gst.FlowOK
					}
					info, ok := sample.GetBuffer().Map(gst.MapRead)
					if !ok {
						return gst.FlowError
					}
					defer info.Unmap()
					// h2642json emits one complete JSON object per buffer.
					var object struct {
						Header struct {
							BFrame bool `json:"direct spatial mv pred flag"`
						} `json:"slice header"`
					}
					if err := json.Unmarshal(info.Data(), &object); err != nil {
						fail(fmt.Errorf("decode H264 metadata: %w", err))
						return gst.FlowError
					}
					track.video.BFrames = track.video.BFrames || object.Header.BFrame
					return gst.FlowOK
				},
			})
		}
		tracks = append(tracks, track)
		if result := pad.Link(bin.GetStaticPad("sink")); result != gst.PadLinkOK {
			fail(fmt.Errorf("link metadata track: %v", result))
			return
		}
		if !bin.SyncStateWithParent() {
			fail(fmt.Errorf("sync metadata track state"))
		}
	})
	defer demux.HandlerDisconnect(handler)
	busErr := make(chan error, 1)
	go func() {
		busErr <- gstpipeline.HandleBusMessages(ctx, pipeline)
		cancel()
	}()
	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		return nil, fmt.Errorf("error setting SegmentMetadata pipeline to playing")
	}
	<-ctx.Done()
	// Stop every native callback before reading the collected track state.
	if pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone) == gst.StateChangeFailure {
		return nil, fmt.Errorf("error setting pipeline state to null")
	}
	busError := <-busErr
	select {
	case err := <-parseErr:
		return nil, err
	default:
	}
	if busError != nil {
		return nil, busError
	}
	sort.Slice(tracks, func(i, j int) bool {
		left, right := tracks[i], tracks[j]
		if (left.video != nil) != (right.video != nil) {
			return left.video != nil
		}
		if left.video != nil {
			leftArea := int64(left.video.Width) * int64(left.video.Height)
			rightArea := int64(right.video.Width) * int64(right.video.Height)
			if leftArea != rightArea {
				return leftArea > rightArea
			}
			if left.video.Width != right.video.Width {
				return left.video.Width > right.video.Width
			}
		}
		return left.id < right.id
	})
	metadata := &localdb.SegmentMediaData{}
	var batch sampleSpan
	for _, track := range tracks {
		if !track.found {
			continue
		}
		if track.span.valid {
			batch.include(gst.ClockTime(track.span.start), track.span.end-track.span.start)
		}
		if track.video != nil {
			if len(metadata.Video) == 0 {
				metadata.Duration = track.duration.Nanoseconds()
			}
			metadata.Video = append(metadata.Video, track.video)
		} else {
			metadata.Audio = append(metadata.Audio, track.audio)
		}
	}
	if len(metadata.Video) == 0 {
		return nil, fmt.Errorf("no video in segment")
	}
	if len(metadata.Audio) == 0 {
		return nil, fmt.Errorf("no audio in segment")
	}
	metadata.BatchDuration = batch.end - batch.start
	return metadata, nil
}

func ParseSegmentMediaDataSinkNewSampleFunc(ctx context.Context, foundThisTrack *bool, duration *time.Duration, span *sampleSpan) func(sink app.AppSink) gst.FlowReturn {
	var firstPTS gst.ClockTime
	haveFirstPTS := false
	return func(sink app.AppSink) gst.FlowReturn {
		sample := sink.PullSample()
		if sample == nil {
			return gst.FlowOK
		}
		buf := sample.GetBuffer()
		if buf == nil {
			return gst.FlowError
		}
		if pts := buf.PTS(); pts != gst.ClockTimeNone {
			if !haveFirstPTS {
				firstPTS = pts
				haveFirstPTS = true
			}
			*duration = time.Duration(int64(pts) - int64(firstPTS))
		}
		if dur, ok := clockTimeDuration(buf.Duration()); ok && dur > 0 {
			*foundThisTrack = true
			timestamp := sample.GetSegment().ToRunningTime(gst.FormatTime, uint64(buf.PTS()))
			span.include(gst.ClockTime(timestamp), dur)
		} else {
			log.Warn(ctx, "no duration found for track", "track", sink.GetName())
		}
		return gst.FlowOK
	}
}
