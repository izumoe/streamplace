package transcoder

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	app "github.com/go-gst/go-gst/pkg/gstapp"
	"stream.place/streamplace/pkg/constants"
	"stream.place/streamplace/pkg/gstinit"
	"stream.place/streamplace/pkg/gstpipeline"
	"stream.place/streamplace/pkg/renditions"
)

const maxRenditionBytes = 64 << 20

type renditionBuffer struct {
	bytes.Buffer
	limit int
}

func (b *renditionBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		return 0, fmt.Errorf("rendition exceeds %d bytes", b.limit)
	}
	return b.Buffer.Write(data)
}

// encode runs in the service's finite job process. The service kills that
// process at its deadline if a native plugin cannot finish shutting down.
func encode(ctx context.Context, source []byte, profiles []renditions.Profile) (outputs [][]byte, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("no rendition profiles")
	}
	gstinit.InitGST()
	lines := []string{
		"appsrc name=src ! qtdemux name=demux",
		"demux.video_0 ! " + constants.Queue2Big + " ! h264parse ! avdec_h264 ! tee name=video",
		"demux.audio_0 ! " + constants.Queue2Big + " ! tee name=audio",
	}
	for i, profile := range profiles {
		if profile.Width <= 0 || profile.Height <= 0 || profile.Bitrate <= 0 ||
			(profile.Profile != "high" && profile.Profile != "baseline") ||
			(profile.FPS == 0) != (profile.FPSDen == 0) {
			return nil, fmt.Errorf("invalid rendition profile %q", profile.Name)
		}
		rate := ""
		if profile.FPS > 0 {
			rate = fmt.Sprintf(" ! videorate drop-only=true ! video/x-raw,framerate=%d/%d", profile.FPS, profile.FPSDen)
		}
		lines = append(lines,
			fmt.Sprintf("video. ! %s ! videoconvertscale ! video/x-raw,format=I420,width=%d,height=%d,pixel-aspect-ratio=1/1%s ! x264enc tune=zerolatency speed-preset=veryfast bframes=0 bitrate=%d ! h264parse ! video/x-h264,profile=%s,stream-format=avc,alignment=au ! mp4mux name=mux%d fragment-mode=0 fragment-duration=1 ! appsink name=sink%d sync=false", constants.Queue2Big, profile.Width, profile.Height, rate, max(1, profile.Bitrate/1000), profile.Profile, i, i),
			fmt.Sprintf("audio. ! %s ! mux%d.audio_0", constants.Queue2Big, i),
		)
	}
	launched, err := gst.ParseLaunch(strings.Join(lines, "\n"))
	if err != nil {
		return nil, fmt.Errorf("create rendition pipeline: %w", err)
	}
	pipeline, ok := launched.(gst.Pipeline)
	if !ok {
		return nil, fmt.Errorf("rendition description is not a pipeline")
	}
	defer func() {
		if pipeline.BlockSetState(gst.StateNull, gst.ClockTime(2*time.Second)) != gst.StateChangeSuccess && err == nil {
			outputs, err = nil, fmt.Errorf("stop rendition pipeline failed")
		}
	}()
	input := pipeline.GetByName("src").(app.AppSrc)
	input.SetCallbacks(&app.AppSrcCallbacks{NeedData: gstpipeline.ReaderNeedDataIncremental(ctx, bytes.NewReader(source))})
	buffers := make([]renditionBuffer, len(profiles))
	for i := range buffers {
		buffers[i].limit = maxRenditionBytes
		sink := pipeline.GetByName(fmt.Sprintf("sink%d", i)).(app.AppSink)
		write := gstpipeline.WriterNewSample(ctx, &buffers[i])
		sink.SetCallbacks(&app.AppSinkCallbacks{NewSample: func(sink app.AppSink) gst.FlowReturn {
			result := write(sink)
			if result != gst.FlowOK {
				sink.PostMessage(gst.NewMessageError(sink, "rendition output failed", fmt.Errorf("rendition output failed or exceeded %d bytes", maxRenditionBytes)))
			}
			return result
		}})
	}
	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		return nil, fmt.Errorf("start rendition pipeline failed")
	}
	if err := gstpipeline.HandleBusMessages(ctx, pipeline); err != nil {
		return nil, err
	}
	outputs = make([][]byte, len(buffers))
	for i := range buffers {
		if buffers[i].Len() == 0 {
			return nil, fmt.Errorf("rendition %q produced no output", profiles[i].Name)
		}
		outputs[i] = buffers[i].Bytes()
	}
	return outputs, nil
}
