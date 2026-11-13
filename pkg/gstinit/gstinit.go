package gstinit

import (
	"sync"

	"github.com/go-gst/go-gst/pkg/gst"
	_ "stream.place/streamplace/pkg/streamplacedeps"
)

var initOnce sync.Once

func InitGST() {
	initOnce.Do(func() {
		gst.Init()
	})
}
