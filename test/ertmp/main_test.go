package main

import (
	"context"
	"testing"
	"time"

	"github.com/bluenviron/gortmplib/pkg/codecs"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/av1"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	muxl "github.com/streamplace/muxl/go"
	"github.com/stretchr/testify/require"
)

func TestFixtureAACConfigurationDescribesEncodedAudio(t *testing.T) {
	engine, err := muxl.NewWASM(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Close(context.Background())) })
	_, audio, err := readFixture(t.Context(), engine, "../fixtures/ertmp-red-h264.mp4")
	require.NoError(t, err)
	// The fixture's decoder-specific information describes mono AAC even
	// though its MP4 sample entry reports two channels.
	var expected mpeg4audio.AudioSpecificConfig
	require.NoError(t, expected.Unmarshal([]byte{0x11, 0x88, 0x56, 0xe5, 0x00}))
	require.Equal(t, expected, *audio.track.Codec.(*codecs.MPEG4Audio).Config)
}

func TestFixtureTracksKeepTheirOwnLoopTimeline(t *testing.T) {
	engine, err := muxl.NewWASM(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Close(context.Background())) })
	tracks, err := readFixtures(t.Context(), engine, "../fixtures")
	require.NoError(t, err)
	require.Len(t, tracks, 4)
	require.IsType(t, &codecs.H264{}, tracks[0].track.Codec)
	require.IsType(t, &codecs.H264{}, tracks[1].track.Codec)
	require.IsType(t, &codecs.AV1{}, tracks[2].track.Codec)
	require.IsType(t, &codecs.MPEG4Audio{}, tracks[3].track.Codec)
	require.NotEmpty(t, tracks[2].av1Config.ConfigOBUs, "the sequence header travels only in sequence-start")
	config := tracks[2].av1Config
	require.EqualValues(t, 1, config.Marker)
	require.EqualValues(t, 1, config.Version)
	var units av1.Bitstream
	require.NoError(t, units.Unmarshal(config.ConfigOBUs))
	var sequence av1.SequenceHeader
	require.NoError(t, sequence.Unmarshal(units[0]))
	require.Equal(t, sequence.SeqProfile, config.SeqProfile)
	require.NotEmpty(t, sequence.SeqLevelIdx)
	require.Equal(t, sequence.SeqLevelIdx[0], config.SeqLevelIdx0, "av1C must describe the encoded sequence, not the writer's placeholder")
	require.Equal(t, 160, sequence.Width())
	require.Equal(t, 120, sequence.Height())
	for _, track := range tracks {
		require.NotEmpty(t, track.samples)
		require.Positive(t, track.duration)
		require.Zero(t, track.samples[0].dts)
		for i, sample := range track.samples {
			require.NotEmpty(t, sample.au)
			if _, av1 := track.track.Codec.(*codecs.AV1); av1 {
				for _, unit := range sample.au {
					require.NotEqualValues(t, 1, (unit[0]>>3)&15, "coded samples must not repeat the sequence header")
				}
			}
			require.GreaterOrEqual(t, sample.pts, sample.dts)
			if i > 0 {
				require.Greater(t, sample.dts, track.samples[i-1].dts)
			}
		}
	}
	// Independent loop lengths preserve AAC's encoded duration rather than
	// padding its timeline to a video's final frame.
	counts := make([]int, len(tracks))
	var last time.Duration
	for range 1000 {
		index, sample := nextSample(tracks)
		require.GreaterOrEqual(t, sample.dts, last)
		last = sample.dts
		counts[index]++
	}
	for i, track := range tracks {
		require.Greater(t, counts[i], len(track.samples)*2, "track %d crosses multiple loop boundaries", i)
	}
}
