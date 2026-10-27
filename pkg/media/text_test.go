package media

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTextRendererUsesEmbeddedFont(t *testing.T) {
	renderer, err := NewTextRenderer()
	require.NoError(t, err)
	data, err := renderer.GenerateImage("streamplace", "#ffffff", "#000000", 16)
	require.NoError(t, err)
	img, err := png.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r > 0 || g > 0 || b > 0 {
				return
			}
		}
	}
	t.Fatal("text renderer produced only the background")
}
