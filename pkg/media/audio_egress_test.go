package media

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/muxl"
)

func TestRTMPSourceKeepsAudioAddendumWithoutRepeatingVideo(t *testing.T) {
	ctx := t.Context()
	cert, key := nodeSignerForTest(t)
	sources := allSignedBareSegments(t, ctx, newBareSegmentSigner(t), getFixture("h264-opus-frag.mp4"))
	mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
	var completed [][]byte
	var tail []byte
	encoder := mm.newStreamTranscoder(ctx, "aac", cert, key, func(_ any, data []byte, audioOnly bool) {
		if audioOnly {
			tail = data
		} else {
			completed = append(completed, data)
		}
	})
	for _, source := range sources {
		require.NoError(t, encoder.Feed(source, nil))
	}
	require.NoError(t, encoder.Close())
	require.NotEmpty(t, tail)
	for _, beforeInit := range []bool{false, true} {
		name := "continuous timeline"
		if beforeInit {
			name = "tail before initialization"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			mm := NewOffline(&config.CLI{})
			mm.bus = bus.NewBus()
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			done := make(chan error, 1)
			const did = "did:example:rtmp-audio"
			stop := context.AfterFunc(ctx, func() { _ = reader.CloseWithError(ctx.Err()) })
			defer stop()
			go func() {
				err := mm.writeRTMPSource(ctx, did, RTMPSourceRendition, writer)
				_ = writer.CloseWithError(err)
				done <- err
			}()
			waitLiveWindowStack(t, "writeRTMPSource", "[select]")
			publish := func(data []byte, audioOnly bool) {
				mm.bus.PublishSegment(ctx, did, RTMPSourceRendition, &bus.Seg{Muxl: data, Published: true, AudioOnly: audioOnly})
			}
			if beforeInit {
				publish(tail, true)
				select {
				case err := <-done:
					require.ErrorContains(t, err, "before RTMP initialization")
				case <-ctx.Done():
					t.Fatal("uninitialized audio continuation was not rejected")
				}
				return
			}
			for i, segment := range completed {
				selected, err := selectSegmentTracks(ctx, segment, 0, "avc1", false)
				require.NoError(t, err)
				var expected bytes.Buffer
				if i == 0 {
					require.NoError(t, muxl.RunMuxlWrapInit(ctx, bytes.NewReader(selected), &expected))
				}
				expected.Write(selected)
				publish(segment, false)
				got := make([]byte, expected.Len())
				_, err = io.ReadFull(reader, got)
				require.NoError(t, err)
				require.Equal(t, expected.Bytes(), got)
			}
			// RTMP carries the AAC continuation only. An Opus continuation for
			// browser/WebRTC playback must not enter the RTMP muxer's input.
			opus, err := MuxlTracks(ctx, sources[len(sources)-1])
			require.NoError(t, err)
			publish(opus["audio2"], true)
			publish(tail, true)
			got := make([]byte, len(tail))
			_, err = io.ReadFull(reader, got)
			require.NoError(t, err)
			require.Equal(t, tail, got, "EOS packets keep their exact signatures and timestamps, without replaying video or initialization")
			cancel()
			require.ErrorIs(t, <-done, context.Canceled)
		})
	}
}
