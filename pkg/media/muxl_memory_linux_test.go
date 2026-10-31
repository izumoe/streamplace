package media

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/muxl"
)

// Each memory setting runs in a fresh test process: the shared WASM engine's
// configuration cannot change after its first operation.
func configureMuxlMemoryTest(tb testing.TB) {
	tb.Helper()
	value := os.Getenv("STREAMPLACE_TEST_MUXL_INITIAL_MB")
	if value == "" {
		tb.Skip("set STREAMPLACE_TEST_MUXL_INITIAL_MB in a fresh test process")
	}
	initial, err := strconv.ParseUint(value, 10, 64)
	require.NoError(tb, err)
	require.Greater(tb, initial, uint64(0))
	require.LessOrEqual(tb, initial, uint64(1024))
	muxl.Configure(initial*1024*1024, 1024*1024*1024)
}

func BenchmarkMuxlMemory(b *testing.B) {
	configureMuxlMemoryTest(b)
	BenchmarkForkUpstream(b)
}

func TestMuxlMemoryCold(t *testing.T) {
	configureMuxlMemoryTest(t)
	TestForkUpstreamBenchmarkCold(t)
}

func BenchmarkAudioCompletionInspection(b *testing.B) {
	configureMuxlMemoryTest(b)
	ctx := context.Background()
	segment := forkBenchmarkSegments(b, "aac")[0]
	valid, err := ValidateMP4Media(ctx, segment)
	require.NoError(b, err)
	mm := &MediaManager{}
	for _, name := range []string{"unwrap", "validated"} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			var target string
			var need bool
			for b.Loop() {
				if name == "unwrap" {
					target, need = mm.audioCompletionTarget(ctx, segment)
				} else {
					target, need = audioCompletionTargetFromMedia(valid.MediaData)
				}
			}
			require.Equal(b, "opus", target)
			require.True(b, need)
		})
	}
}

func TestMuxlMemory(t *testing.T) {
	configureMuxlMemoryTest(t)
	for _, test := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"bare", TestValidateMP4MediaBareSegment},
		{"av1-completion", TestAV1AudioCompletionPreservesSignedVideo},
		{"multitrack", TestRTMPMultitrackCompletionAndExtraction},
	} {
		t.Run(test.name, test.run)
	}
}
