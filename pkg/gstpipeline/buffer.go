package gstpipeline

import (
	"fmt"

	"github.com/go-gst/go-gst/pkg/gst"
)

// NewBufferFromBytes copies bs into a freshly allocated buffer.
func NewBufferFromBytes(bs []byte) (*gst.Buffer, error) {
	buffer := gst.NewBufferAllocate(nil, uint(len(bs)), nil)
	info, ok := buffer.Map(gst.MapWrite)
	if !ok {
		return nil, fmt.Errorf("failed to map buffer for writing")
	}
	defer info.Unmap()
	copy(info.Data(), bs)
	return buffer, nil
}
