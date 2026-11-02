package media

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	app "github.com/go-gst/go-gst/pkg/gstapp"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"stream.place/streamplace/pkg/gstpipeline"
	"stream.place/streamplace/pkg/log"
)

func TestWriterNewSample(t *testing.T) {
	withNoGSTLeaks(t, func() {
		filePath := getFixture("5sec.mp4")
		fileInfo, err := os.Stat(filePath)
		require.NoError(t, err)
		fileSize := fileInfo.Size()
		t.Logf("Test file size: %d bytes", fileSize)
		g, ctx := errgroup.WithContext(context.Background())
		// ctx = log.WithDebugValue(ctx, map[string]map[string]int{"func": {"TestWriterNewSample": 9}})
		for i := 0; i < streamplaceTestCount; i++ {
			g.Go(func() error {
				bs := bytes.Buffer{}
				err := writerNewSampleInner(ctx, i, &bs)
				if err != nil {
					return err
				}
				if bs.Len() != int(fileSize) {
					return fmt.Errorf("expected %d bytes, got %d", fileSize, bs.Len())
				}
				return nil
			})
		}
		err = g.Wait()
		require.NoError(t, err)
	})
}

// A live tiny allocation can pin a neighboring GstSample wrapper indefinitely.
// Keep the writer's markers alive through the native leak check, not just EOS.
func TestWriterNewSampleTinyAllocations(t *testing.T) {
	writer := &tinyAllocWriter{}
	withNoGSTLeaks(t, func() {
		require.NoError(t, writerNewSampleInner(context.Background(), 0, writer))
	})
	runtime.KeepAlive(writer)
}

func BenchmarkWriterNewSample(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if err := writerNewSampleInner(b.Context(), 0, io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}

func TestWriterNewSampleReleasesBufferBeforeGC(t *testing.T) {
	previousGC := debug.SetGCPercent(-1)
	t.Cleanup(func() { debug.SetGCPercent(previousGC) })
	previousVerbosity := flag.Lookup("v").Value.String()
	require.NoError(t, flag.Set("v", "1"))
	t.Cleanup(func() { require.NoError(t, flag.Set("v", previousVerbosity)) })
	for _, failWrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("write-error=%t", failWrite), func(t *testing.T) {
			var logs logCapture
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			payload, err := os.ReadFile(getFixture("sample-segment.mp4"))
			require.NoError(t, err)
			var output bytes.Buffer
			var writer io.Writer = &output
			if failWrite {
				closed, err := os.CreateTemp(t.TempDir(), "closed-output")
				require.NoError(t, err)
				require.NoError(t, closed.Close())
				writer = closed
			}
			element, err := gst.ParseLaunch("appsrc name=src ! appsink name=sink sync=false enable-last-sample=false")
			require.NoError(t, err)
			pipeline := element.(gst.Pipeline)
			defer pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone)
			sink := pipeline.GetByName("sink").(app.AppSink)
			heldCh := make(chan *gst.Buffer, 1)
			sink.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
				// GetBuffer owns one reference independently of the pulled sample.
				heldCh <- info.GetBuffer()
				return gst.PadProbeOK
			})
			sink.SetCallbacks(&app.AppSinkCallbacks{NewSample: gstpipeline.WriterNewSample(ctx, writer)})
			errCh := make(chan error, 1)
			go func() { errCh <- gstpipeline.HandleBusMessages(ctx, pipeline) }()
			require.NotEqual(t, gst.StateChangeFailure, pipeline.SetState(gst.StatePlaying))
			input, err := gstpipeline.NewBufferFromBytes(payload)
			require.NoError(t, err)
			src := pipeline.GetByName("src").(app.AppSrc)
			require.Equal(t, gst.FlowOK, src.PushBuffer(input))
			require.Equal(t, gst.FlowOK, src.EndOfStream())
			if failWrite {
				require.ErrorContains(t, <-errCh, "gstreamer error")
				require.Contains(t, logs.String(), "error writing to output")
			} else {
				require.NoError(t, <-errCh)
				require.Equal(t, payload, output.Bytes())
				require.Empty(t, logs.String())
			}
			require.Equal(t, gst.StateChangeSuccess, pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone))
			var held *gst.Buffer
			select {
			case held = <-heldCh:
			case <-ctx.Done():
				t.Fatal("buffer never reached the sink")
			}
			require.NotNil(t, held)
			defer gst.UnsafeBufferUnref(held)
			require.True(t, gst.UnsafeMiniObjectFromGlibBorrow(gst.UnsafeBufferToGlibNone(held)).IsWritable(),
				"the callback must release its sample without waiting for Go GC")
		})
	}
}

type tinyAllocWriter struct {
	bytes.Buffer
	markers []*uint64
}

func (w *tinyAllocWriter) Write(p []byte) (int, error) {
	marker := new(uint64)
	*marker = uint64(len(p))
	w.markers = append(w.markers, marker)
	return w.Buffer.Write(p)
}

func writerNewSampleInner(ctx context.Context, i int, w io.Writer) error {
	ctx = log.WithLogValues(ctx, "func", "TestWriterNewSample")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	pipeline := gst.NewPipeline(fmt.Sprintf("TestWriterNewSample-%d", i)).(gst.Pipeline)

	fileSrc := gst.ElementFactoryMakeWithProperties("filesrc", map[string]any{
		"location": getFixture("5sec.mp4"),
	})
	if fileSrc == nil {
		return fmt.Errorf("failed to create filesrc")
	}
	if !pipeline.Add(fileSrc) {
		return fmt.Errorf("failed to add filesrc")
	}

	var busErr error
	go func() {
		busErr = gstpipeline.HandleBusMessages(ctx, pipeline)
		cancel()
	}()

	appSink := gst.ElementFactoryMakeWithProperties("appsink", map[string]any{
		"name": fmt.Sprintf("TestWriterNewSample-appsink-%d", i),
		"sync": false,
	})
	if appSink == nil {
		return fmt.Errorf("failed to create appsink")
	}
	if !pipeline.Add(appSink) {
		return fmt.Errorf("failed to add appsink")
	}

	sink := appSink.(app.AppSink)
	sink.SetCallbacks(&app.AppSinkCallbacks{
		NewSample: gstpipeline.WriterNewSample(ctx, w),
	})

	if !fileSrc.Link(appSink) {
		return fmt.Errorf("failed to link filesrc to appsink")
	}

	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		return fmt.Errorf("failed to start writer pipeline")
	}

	<-ctx.Done()

	if pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone) != gst.StateChangeSuccess {
		return fmt.Errorf("failed to stop writer pipeline")
	}

	return busErr
}
