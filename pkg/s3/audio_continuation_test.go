package s3

import (
	"bytes"
	"flag"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLiveUploadIndexesSourceAndAudioTailTogether(t *testing.T) {
	for _, explicitCutover := range []bool{false, true} {
		name := "one object"
		if explicitCutover {
			name = "explicit cutover after each source"
		}
		t.Run(name, func(t *testing.T) {
			recorder := &fakeRecorder{}
			uploader, store := liveHTTPUploader(t, false, time.Hour, recorder)
			start := time.UnixMilli(1700000000000)
			var bundles [][]byte
			var index []RecordedSegment
			for i, source := range [][]byte{[]byte("source-a"), []byte("source-b")} {
				stamp := start.Add(time.Duration(i) * time.Second)
				tail := []byte("encoded-audio-tail")
				require.NoError(t, uploader.TryAddSegment(t.Context(), source, stamp))
				// The index clock is milliseconds, including when the command's
				// time.Time carries finer precision than the source metadata.
				require.NoError(t, uploader.TryAddAudioAddendum(t.Context(), tail, stamp.Add(500*time.Microsecond)))
				bundle := append(bytes.Clone(source), tail...)
				bundles = append(bundles, bundle)
				index = append(index, RecordedSegment{StartMs: stamp.UnixMilli(), Size: int64(len(bundle))})
				if explicitCutover {
					require.NoError(t, uploader.TryCutover(t.Context()))
				}
			}
			require.NoError(t, uploader.Close(t.Context()))
			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			store.mu.Lock()
			defer store.mu.Unlock()
			if explicitCutover {
				require.Equal(t, bundles, store.objects)
				require.Equal(t, [][]RecordedSegment{{index[0]}, {index[1]}}, recorder.indexes)
			} else {
				require.Equal(t, [][]byte{bytes.Join(bundles, nil)}, store.objects)
				require.Equal(t, [][]RecordedSegment{index}, recorder.indexes)
			}
		})
	}
}

func TestLiveUploadDefersAutomaticCutoverUntilNextSource(t *testing.T) {
	for _, chapter := range []bool{false, true} {
		name, cutoverEvery := "object age", time.Nanosecond
		if chapter {
			name, cutoverEvery = "livestream chapter", time.Hour
		}
		t.Run(name, func(t *testing.T) {
			recorder := &fakeRecorder{}
			uploader, store := liveHTTPUploader(t, false, cutoverEvery, recorder)
			start := time.UnixMilli(1700000000000)
			source, tail, next := []byte("source-video-and-audio"), []byte("encoded-audio-tail"), []byte("next-source-video-and-audio")
			uploader.SetLivestreamURI("at://chapter-a")
			require.NoError(t, uploader.TryAddSegment(t.Context(), source, start))
			waitForStarts(t, recorder, 1)
			if chapter {
				uploader.SetLivestreamURI("at://chapter-b")
			}
			require.NoError(t, uploader.TryAddAudioAddendum(t.Context(), tail, start))
			require.NoError(t, uploader.TryAddSegment(t.Context(), next, start.Add(time.Second)))
			require.NoError(t, uploader.Close(t.Context()))
			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			store.mu.Lock()
			defer store.mu.Unlock()
			require.Equal(t, [][]byte{append(bytes.Clone(source), tail...), next}, store.objects, "an automatic boundary must not strand the EOS tail in an audio-only object")
			require.Equal(t, [][]RecordedSegment{
				{{StartMs: start.UnixMilli(), Size: int64(len(source) + len(tail))}},
				{{StartMs: start.Add(time.Second).UnixMilli(), Size: int64(len(next))}},
			}, recorder.indexes, "clips must select each source together with its encoded audio tail")
			uris := []string{"at://chapter-a", "at://chapter-a"}
			if chapter {
				uris[1] = "at://chapter-b"
			}
			require.Equal(t, uris, recorder.uris)
		})
	}
}

func TestLiveUploadDropsAudioWhoseSourceIsNoLongerOpen(t *testing.T) {
	for _, mode := range []string{"explicit cutover", "failed source upload", "different source already open"} {
		t.Run(mode, func(t *testing.T) {
			recorder := &fakeRecorder{}
			uploader, store := liveHTTPUploader(t, false, time.Hour, recorder)
			start := time.UnixMilli(1700000000000)
			source, next, tail := []byte("source-a"), []byte("source-b"), []byte("encoded-audio-tail")
			if mode == "failed source upload" {
				source = bytes.Repeat([]byte{1}, minPartSize+1)
				store.mu.Lock()
				store.failNextPart = true
				store.mu.Unlock()
			}
			var logs bytes.Buffer
			previousLogger := slog.Default()
			previousVerbosity := flag.Lookup("v").Value.String()
			require.NoError(t, flag.Set("v", "2"))
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer func() {
				slog.SetDefault(previousLogger)
				require.NoError(t, flag.Set("v", previousVerbosity))
			}()
			require.NoError(t, uploader.TryAddSegment(t.Context(), source, start))
			if mode != "failed source upload" {
				require.NoError(t, uploader.TryCutover(t.Context()))
			}
			if mode == "different source already open" {
				require.NoError(t, uploader.TryAddSegment(t.Context(), next, start.Add(time.Second)))
			}
			require.NoError(t, uploader.TryAddAudioAddendum(t.Context(), tail, start))
			if mode != "different source already open" {
				require.NoError(t, uploader.TryAddSegment(t.Context(), next, start.Add(time.Second)))
			}
			require.NoError(t, uploader.TryAddAudioAddendum(t.Context(), tail, start.Add(time.Second)))
			require.NoError(t, uploader.Close(t.Context()))
			if mode == "failed source upload" {
				require.Contains(t, logs.String(), "abandoning live-rec S3 object")
			}
			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			store.mu.Lock()
			defer store.mu.Unlock()
			objects := [][]byte{append(bytes.Clone(next), tail...)}
			indexes := [][]RecordedSegment{{{StartMs: start.Add(time.Second).UnixMilli(), Size: int64(len(next) + len(tail))}}}
			if mode != "failed source upload" {
				objects = append([][]byte{source}, objects...)
				indexes = append([][]RecordedSegment{{{StartMs: start.UnixMilli(), Size: int64(len(source))}}}, indexes...)
			}
			require.Equal(t, objects, store.objects, "orphan audio must not create or corrupt a recorded object")
			require.Equal(t, indexes, recorder.indexes)
			require.Contains(t, logs.String(), "without recorded source")
		})
	}
}
