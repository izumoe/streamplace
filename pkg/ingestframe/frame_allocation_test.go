//go:build !race

package ingestframe

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// Allocation budgets measure ordinary builds. The race detector deliberately
// drops sync.Pool entries, including the encoder's reusable scratch buffers.
func TestWriterAllocationBudget(t *testing.T) {
	payload := bytes.Repeat([]byte{0xab}, 1024*1024)
	writer := NewWriter(io.Discard)
	result := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if err := writer.SourceSegment(payload); err != nil {
				b.Fatal(err)
			}
		}
	})
	require.Less(t, result.AllocedBytesPerOp(), int64(64*1024), "a 1 MiB frame must not require another payload-sized allocation")
}
