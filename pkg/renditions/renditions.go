package renditions

import (
	"fmt"
	"math"
	"strings"

	"stream.place/streamplace/pkg/placestream"
)

type FPS struct {
	Passthrough bool
	Num         uint
	Den         uint
}

type Rendition struct {
	Width     int64
	Height    int64
	Bitrate   int
	Framerate FPS
	Profile   string
	Name      string
	Parent    *Rendition
	AudioOnly bool
}

var AudioRendition = Rendition{
	Name:      "audio",
	Bitrate:   128_000,
	AudioOnly: true,
}

type Profile struct {
	Name    string `json:"name,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Bitrate int    `json:"bitrate,omitempty"`
	FPS     uint   `json:"fps,omitempty"`
	FPSDen  uint   `json:"fpsDen,omitempty"`
	Profile string `json:"profile,omitempty"`
}

// ToProfile preserves the fitted aspect ratio and rounds dimensions to even
// values for the encoder.
func (r Rendition) ToProfile() Profile {
	return Profile{
		Name:    r.Name,
		Width:   even(r.Width),
		Height:  even(r.Height),
		Bitrate: r.Bitrate,
		FPS:     r.Framerate.Num,
		FPSDen:  r.Framerate.Den,
		Profile: r.Profile,
	}
}

func even(n int64) int {
	if n%2 != 0 {
		n++
	}
	return int(n)
}

type Renditions []Rendition

func (rs Renditions) ToProfiles() []Profile {
	profiles := make([]Profile, len(rs))
	for i, r := range rs {
		profiles[i] = r.ToProfile()
	}
	return profiles
}

var DesiredRenditions = []Rendition{
	{
		Name:    "1080p",
		Width:   1920,
		Height:  1080,
		Bitrate: 6_000_000,
		Framerate: FPS{
			Num: 60,
			Den: 1,
		},
		Profile: "high",
	},
	{
		Name:    "720p",
		Width:   1280,
		Height:  720,
		Bitrate: 3_000_000,
		Framerate: FPS{
			Num: 60,
			Den: 1,
		},
		Profile: "high",
	},
	{
		Name:    "360p",
		Width:   640,
		Height:  360,
		Bitrate: 1_000_000,
		Framerate: FPS{
			Num: 30,
			Den: 1,
		},
		Profile: "high",
	},
	{
		Name:    "240p",
		Width:   426,
		Height:  240,
		Bitrate: 500_000,
		Framerate: FPS{
			Num: 30,
			Den: 1,
		},
		Profile: "high",
	},
	{
		Name:    "160p",
		Width:   284,
		Height:  160,
		Bitrate: 250_000,
		Framerate: FPS{
			Num: 30,
			Den: 1,
		},
		Profile: "baseline",
	},
}

// Ladder resolves a comma-separated selection of rendition names ("240p,160p")
// from DesiredRenditions, in ladder order; empty means the whole ladder. An
// unknown name is an error.
func Ladder(spec string) ([]Rendition, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return DesiredRenditions, nil
	}
	want := map[string]bool{}
	for _, n := range strings.Split(spec, ",") {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		known := false
		for _, r := range DesiredRenditions {
			if r.Name == n {
				known = true
			}
		}
		if !known {
			names := make([]string, 0, len(DesiredRenditions))
			for _, r := range DesiredRenditions {
				names = append(names, r.Name)
			}
			return nil, fmt.Errorf("unknown rendition %q (known: %s)", n, strings.Join(names, ", "))
		}
		want[n] = true
	}
	out := make([]Rendition, 0, len(want))
	for _, r := range DesiredRenditions {
		if want[r.Name] {
			out = append(out, r)
		}
	}
	return out, nil
}

// GenerateRenditions generates renditions for a given spseg from the whole
// ladder.
func GenerateRenditions(spseg *placestream.Segment) (Renditions, error) {
	return GenerateRenditionsFrom(spseg, DesiredRenditions)
}

// GenerateRenditionsFrom generates the renditions of ladder (see Ladder)
// that fit under spseg's video.
func GenerateRenditionsFrom(spseg *placestream.Segment, ladder []Rendition) (Renditions, error) {
	if len(spseg.Video) == 0 {
		return nil, fmt.Errorf("no video stream found")
	}
	vid := spseg.Video[0]
	for _, source := range spseg.Video {
		if len(spseg.Video) > 1 && (source.Codec == "h264" || strings.HasPrefix(source.Codec, "avc1")) {
			return nil, nil
		}
		if source.Width*source.Height > vid.Width*vid.Height || source.Width*source.Height == vid.Width*vid.Height && source.Width > vid.Width {
			vid = source
		}
	}
	var framerate FPS
	if vid.Framerate != nil && vid.Framerate.Num > 0 && vid.Framerate.Den > 0 {
		framerate = FPS{Num: uint(vid.Framerate.Num), Den: uint(vid.Framerate.Den)}
	}
	rs := []Rendition{}
	for _, r := range ladder {
		vidWidth := int64(vid.Width)
		vidHeight := int64(vid.Height)
		vertical := vid.Height > vid.Width
		// do all the math as if it's horizontal then flip at the end
		if vertical {
			vidWidth, vidHeight = vidHeight, vidWidth
		}
		if vidWidth <= r.Width && vidHeight <= r.Height {
			continue
		}
		rAspectRatio := float64(r.Width) / float64(r.Height)
		vidAspectRatio := float64(vidWidth) / float64(vidHeight)
		if vidAspectRatio > rAspectRatio {
			// vid is wider than r
			// scale down to r.Width
			scale := float64(r.Width) / float64(vidWidth)
			vidWidth = r.Width
			vidHeight = int64(math.Round(float64(vidHeight) * scale))
		} else {
			// vid is taller than r
			// scale down to r.Height
			scale := float64(r.Height) / float64(vidHeight)
			vidHeight = r.Height
			vidWidth = int64(math.Round(float64(vidWidth) * scale))
		}
		outR := Rendition{
			Name:    r.Name,
			Parent:  &r,
			Profile: r.Profile,
		}
		if vertical {
			outR.Width = vidHeight
			outR.Height = vidWidth
		} else {
			outR.Width = vidWidth
			outR.Height = vidHeight
		}

		// if vertical {
		// 	ratio := float64(r.Height) / float64(vid.Height)
		// 	outR.Height = int64(float64(vid.Width) * (16.0 / 9.0) * ratio)
		// 	outR.Width = r.Height
		// } else {
		// 	ratio := float64(r.Width) / float64(vid.Width)
		// 	outR.Width = r.Width
		// 	outR.Height = int64(float64(vid.Width) * (9.0 / 16.0) * ratio)
		// }
		if framerate.Den > 0 {
			vidFPS := float64(framerate.Num) / float64(framerate.Den)
			rFPS := float64(r.Framerate.Num) / float64(r.Framerate.Den)
			delta := rFPS / vidFPS

			if rFPS < vidFPS {
				if delta < 0.75 {
					outR.Framerate.Num = uint(framerate.Num)
					outR.Framerate.Den = uint(framerate.Den * 2)
				}
			}
		}

		outR.Bitrate = r.Bitrate
		outR.Profile = r.Profile
		rs = append(rs, outR)
	}
	if len(rs) == 0 && (vid.Codec == "av1" || strings.HasPrefix(vid.Codec, "av01")) {
		rs = append(rs, Rendition{Name: fmt.Sprintf("%dp", vid.Height), Width: vid.Width, Height: vid.Height, Bitrate: 500_000, Profile: "baseline", Framerate: framerate})
	}
	return rs, nil
}
