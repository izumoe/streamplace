package media

import (
	"context"
	"io"
	"runtime/debug"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	app "github.com/go-gst/go-gst/pkg/gstapp"
	"stream.place/streamplace/pkg/gstpipeline"

	"github.com/stretchr/testify/require"
)

func TestAUHasIDR(t *testing.T) {
	annexB := func(types ...byte) []byte {
		var b []byte
		for _, ty := range types {
			b = append(b, 0, 0, 0, 1, ty, 0xaa, 0xbb)
		}
		return b
	}
	avc := func(types ...byte) []byte {
		var b []byte
		for _, ty := range types {
			b = append(b, 0, 0, 0, 3, ty, 0xaa, 0xbb)
		}
		return b
	}
	require.True(t, auHasIDR(annexB(9, 7, 8, 5)), "IDR with headers, Annex B")
	require.True(t, auHasIDR(avc(7, 8, 5)), "IDR with headers, length-prefixed")
	require.False(t, auHasIDR(annexB(9, 1)), "a non-IDR I- or P-slice")
	require.False(t, auHasIDR(avc(1)), "length-prefixed non-IDR")
	require.False(t, auHasIDR(avc(6, 1)), "SEI then non-IDR slice")
	require.False(t, auHasIDR(nil))
	require.False(t, auHasIDR([]byte{1, 2, 3, 4, 5, 6}), "garbage is not an IDR")
	require.Equal(t, []int{7, 8, 5, 1}, h264NALTypes(avc(7, 8, 5, 1)))
}

func TestIDRKeyframeProbeReleasesBuffersBeforeGC(t *testing.T) {
	previousGC := debug.SetGCPercent(-1)
	t.Cleanup(func() { debug.SetGCPercent(previousGC) })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	element, err := gst.ParseLaunch("videotestsrc num-buffers=3 ! video/x-raw,width=160,height=120,framerate=30/1 ! x264enc tune=zerolatency speed-preset=ultrafast key-int-max=30 ! h264parse ! identity name=probe ! appsink name=sink sync=false enable-last-sample=false")
	require.NoError(t, err)
	pipeline := element.(gst.Pipeline)
	defer pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone)
	probe := pipeline.GetByName("probe")
	index := 0
	probe.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		buffer := info.GetBuffer()
		if buffer == nil {
			return gst.PadProbeOK
		}
		defer gst.UnsafeBufferUnref(buffer)
		// Keep one genuine delta frame and falsely mark the next as a keyframe.
		if index == 2 {
			buffer.UnsetFlags(gst.BufferFlagDeltaUnit)
		}
		index++
		return gst.PadProbeOK
	})
	installIDRKeyframeProbe(ctx, probe.GetStaticPad("src"), "test")
	heldCh := make(chan *gst.Buffer, 3)
	sink := pipeline.GetByName("sink").(app.AppSink)
	sink.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		heldCh <- info.GetBuffer()
		return gst.PadProbeOK
	})
	sink.SetCallbacks(&app.AppSinkCallbacks{NewSample: gstpipeline.WriterNewSample(ctx, io.Discard)})
	require.NotEqual(t, gst.StateChangeFailure, pipeline.SetState(gst.StatePlaying))
	require.NoError(t, gstpipeline.HandleBusMessages(ctx, pipeline))
	require.Equal(t, gst.StateChangeSuccess, pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone))
	defer func() {
		for len(heldCh) > 0 {
			if buffer := <-heldCh; buffer != nil {
				gst.UnsafeBufferUnref(buffer)
			}
		}
	}()
	require.Len(t, heldCh, 3)
	for i := range 3 {
		buffer := <-heldCh
		require.NotNil(t, buffer)
		defer gst.UnsafeBufferUnref(buffer)
		payload, err := copyBufferData(buffer)
		require.NoError(t, err)
		require.Equal(t, i == 0, auHasIDR(payload), "the encoded fixture must contain one IDR followed by two non-IDR frames")
		require.Equal(t, i != 0, buffer.HasFlags(gst.BufferFlagDeltaUnit), "only the real IDR remains a keyframe")
		require.True(t, gst.UnsafeMiniObjectFromGlibBorrow(gst.UnsafeBufferToGlibNone(buffer)).IsWritable(), "the IDR probe must release its reference before Go GC, frame %d", i)
	}
}
