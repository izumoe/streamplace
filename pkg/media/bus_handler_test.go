package media

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	app "github.com/go-gst/go-gst/pkg/gstapp"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"stream.place/streamplace/pkg/gstinit"
	"stream.place/streamplace/pkg/log"
)

type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func TestBusHandlerCleanup(t *testing.T) {
	withNoGSTLeaks(t, func() {

		g, ctx := errgroup.WithContext(context.Background())
		ctx = log.WithDebugValue(ctx, map[string]map[string]int{"func": {"TestBusHandler": 9}})
		for i := range streamplaceTestCount {
			g.Go(func() error {
				err := testBusHandlerCleanupInner(ctx, i)
				if err == nil {
					return fmt.Errorf("expected error")
				}
				return nil
			})
		}
		err := g.Wait()
		require.NoError(t, err)
	})
}

func testBusHandlerCleanupInner(ctx context.Context, i int) error {
	ctx = log.WithLogValues(ctx, "func", "TestBusHandler")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	pipeline := gst.NewPipeline(fmt.Sprintf("TestBusHandler-%d", i)).(gst.Pipeline)

	busDone := make(chan struct{})
	go func() {
		_ = HandleBusMessages(ctx, pipeline)
		busDone <- struct{}{}
		cancel()
	}()

	defer func() {
		cancel()
		<-busDone
		if pipeline.BlockSetState(gst.StateNull, gst.ClockTimeNone) != gst.StateChangeSuccess {
			panic("failed to set state to null")
		}
	}()

	fileSrc := gst.ElementFactoryMakeWithProperties("filesrc", map[string]any{
		"location": getFixture("5sec.mp4"),
	})
	if fileSrc == nil {
		return fmt.Errorf("failed to create filesrc")
	}
	if !pipeline.Add(fileSrc) {
		return fmt.Errorf("failed to add filesrc")
	}

	demux := gst.ElementFactoryMakeWithProperties("qtdemux", map[string]any{
		"name": fmt.Sprintf("TestBusHandler-qtdemux-%d", i),
	})
	if demux == nil {
		return fmt.Errorf("failed to create demux")
	}
	if !pipeline.Add(demux) {
		return fmt.Errorf("failed to add demux")
	}

	appSink := gst.ElementFactoryMakeWithProperties("appsink", map[string]any{
		"name": fmt.Sprintf("TestBusHandler-appsink-%d", i),
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
		NewSample: WriterNewSample(ctx, nil),
	})

	return fmt.Errorf("test error")

}

func TestBusHandlerTagMessages(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		verbosity    string
		debugContext bool
	}{
		{name: "disabled", verbosity: "0"},
		{name: "global", verbosity: "4"},
		{name: "context", verbosity: "0", debugContext: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var logs logCapture
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })
			previousVerbosity := flag.Lookup("v").Value.String()
			require.NoError(t, flag.Set("v", testCase.verbosity))
			t.Cleanup(func() { require.NoError(t, flag.Set("v", previousVerbosity)) })

			withNoGSTLeaks(t, func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if testCase.debugContext {
					ctx = log.WithLogValues(ctx, "func", "TestBusHandlerTagMessages")
					ctx = log.WithDebugValue(ctx, map[string]map[string]int{"func": {"TestBusHandlerTagMessages": 4}})
				}
				pipeline := gst.NewPipeline("bus-message-test").(gst.Pipeline)
				tags := gst.NewTagListFromString("taglist, title=(string)streamplace-bus-tag-payload;")
				require.NotNil(t, tags)
				tagMessage := gst.NewMessageTag(pipeline, tags)
				eosMessage := gst.NewMessageEOS(pipeline)
				require.NotNil(t, tagMessage)
				require.NotNil(t, eosMessage)
				bus := pipeline.GetBus()
				const tagCount = 32
				for range tagCount {
					require.True(t, bus.Post(tagMessage.Copy()))
				}
				require.True(t, bus.Post(eosMessage.Copy()))
				seen := make([]gst.MessageType, 0, tagCount+1)
				err := HandleBusMessagesCustom(ctx, pipeline, func(msg *gst.Message) {
					seen = append(seen, msg.Type())
				})
				require.NoError(t, err)
				require.Len(t, seen, tagCount+1)
				for _, messageType := range seen[:tagCount] {
					require.Equal(t, gst.MessageTag, messageType)
				}
				require.Equal(t, gst.MessageEOS, seen[tagCount])
			})
			if testCase.verbosity == "0" && !testCase.debugContext {
				require.Empty(t, logs.String())
			} else {
				require.Contains(t, logs.String(), "type="+gst.MessageTag.String())
				require.NotContains(t, logs.String(), "streamplace-bus-tag-payload")
			}
		})
	}
}

func BenchmarkBusMessageHandling(b *testing.B) {
	gstinit.InitGST()
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(previousLogger) })
	previousVerbosity := flag.Lookup("v").Value.String()
	require.NoError(b, flag.Set("v", "0"))
	b.Cleanup(func() { require.NoError(b, flag.Set("v", previousVerbosity)) })
	pipeline := gst.NewPipeline("bus-message-benchmark").(gst.Pipeline)
	tags := gst.NewTagListFromString("taglist, title=(string)streamplace-bus-tag-payload;")
	require.NotNil(b, tags)
	tagMessage := gst.NewMessageTag(pipeline, tags)
	eosMessage := gst.NewMessageEOS(pipeline)
	require.NotNil(b, tagMessage)
	require.NotNil(b, eosMessage)
	bus := pipeline.GetBus()
	const tagCount = 32
	b.ReportAllocs()
	for b.Loop() {
		for range tagCount {
			if !bus.Post(tagMessage.Copy()) {
				b.Fatal("failed to post tag message")
			}
		}
		if !bus.Post(eosMessage.Copy()) {
			b.Fatal("failed to post EOS message")
		}
		if err := HandleBusMessages(context.Background(), pipeline); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(tagCount+1, "messages/op")
}
