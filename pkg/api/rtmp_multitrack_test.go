package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/abema/go-mp4"
	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/codecs"
	"github.com/bluenviron/gortmplib/pkg/message"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/av1"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/media"
)

// The publisher and reader use the real RTMP handshake and message codecs. The
// two-second timestamp span completes gortmplib's track discovery without a
// wall-clock wait.
func readRTMPPublisher(t *testing.T, publish func(gortmplib.Conn) error) (*media.RTMPSession, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	u, err := url.Parse("rtmp://" + listener.Addr().String() + "/live/test")
	require.NoError(t, err)
	publisherDone := make(chan error, 1)
	go func() {
		client := &gortmplib.Client{URL: u, Publish: true}
		if err := client.Initialize(ctx); err != nil {
			publisherDone <- err
			return
		}
		defer client.NetConn().Close()
		if err := client.NetConn().SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			publisherDone <- err
			return
		}
		publisherDone <- publish(client)
	}()
	conn, err := listener.Accept()
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
	sc := &gortmplib.ServerConn{RW: conn}
	require.NoError(t, sc.Initialize())
	require.NoError(t, sc.AcceptConn())
	session := &media.RTMPSession{EventChan: make(chan any, 1024)}
	reader, err := initializeRTMPReader(ctx, sc, session)
	if err != nil {
		// Admission can reject while the publisher is still writing discovery
		// messages. Closing the connection releases that writer before joining it.
		_ = conn.Close()
		return session, errors.Join(err, <-publisherDone)
	}
	for {
		err = reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = conn.Close()
			return session, errors.Join(err, <-publisherDone)
		}
	}
	require.NoError(t, <-publisherDone)
	close(session.EventChan)
	return session, nil
}

func publishRTMPTracks(tracks []*gortmplib.Track) func(gortmplib.Conn) error {
	return func(conn gortmplib.Conn) error {
		writer := &gortmplib.Writer{Conn: conn, Tracks: tracks}
		if err := writer.Initialize(); err != nil {
			return err
		}
		for _, stamp := range []time.Duration{0, 2 * time.Second} {
			for _, track := range tracks {
				var err error
				switch track.Codec.(type) {
				case *codecs.H264:
					pts := stamp
					if stamp == 0 {
						pts += 33 * time.Millisecond
					}
					err = writer.WriteH264(track, pts, stamp, [][]byte{{0x65, 0x88, 0x84, 0x00}})
				case *codecs.MPEG4Audio:
					err = writer.WriteMPEG4Audio(track, stamp, []byte{0x21, 0x10, 0x04, 0x60, 0x8c, 0x1c})
				case *codecs.VP9:
					err = writer.WriteVP9(track, stamp, []byte{0x82})
				default:
					return fmt.Errorf("test publisher codec: %T", track.Codec)
				}
				if err != nil {
					return err
				}
			}
		}
		return nil
	}
}

func rtmpH264Track() *gortmplib.Track {
	return &gortmplib.Track{Codec: &codecs.H264{SPS: testSPS, PPS: []byte{0x68, 0xce, 0x3c, 0x80}}}
}

func rtmpAACTrack() *gortmplib.Track {
	return &gortmplib.Track{Codec: &codecs.MPEG4Audio{Config: &mpeg4audio.AudioSpecificConfig{
		Type: 2, SampleRate: 48000, ChannelConfig: 2,
	}}}
}

func TestRTMPPublishConnClosesRejectedConnection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		publish bool
		error   string
	}{
		{"invalid publish path", "/other/test", true, "must start with /live/"},
		{"playback", "/live/test", false, "playback is not allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer listener.Close()
			u, err := url.Parse("rtmp://" + listener.Addr().String() + tc.path)
			require.NoError(t, err)
			client := &gortmplib.Client{URL: u, Publish: tc.publish}
			clientDone := make(chan struct{})
			var clientErr error
			go func() {
				clientErr = client.Initialize(ctx)
				close(clientDone)
			}()
			conn, err := listener.Accept()
			require.NoError(t, err)
			t.Cleanup(func() {
				_ = conn.Close()
				cancel()
				<-clientDone
			})
			api := &StreamplaceAPI{}
			require.ErrorContains(t, api.HandleRTMPPublishConn(ctx, conn), tc.error)
			select {
			case <-clientDone:
				require.ErrorIs(t, clientErr, io.EOF)
			case <-time.After(time.Second):
				t.Fatal("rejected RTMP connection remained open after its handler returned")
			}
		})
	}
}

func TestRTMPReaderRejectsUnsupportedTrackLayouts(t *testing.T) {
	tooMany := make([]*gortmplib.Track, 0, 8)
	for range 7 {
		tooMany = append(tooMany, rtmpH264Track())
	}
	tooMany = append(tooMany, rtmpAACTrack())
	for _, tc := range []struct {
		name   string
		tracks []*gortmplib.Track
		error  string
	}{
		{"no video", []*gortmplib.Track{rtmpAACTrack()}, "video"},
		{"no audio", []*gortmplib.Track{rtmpH264Track()}, "AAC"},
		{"multiple audio", []*gortmplib.Track{rtmpH264Track(), rtmpAACTrack(), rtmpAACTrack()}, "audio"},
		{"too many videos", tooMany, "6"},
		{"unsupported codec", []*gortmplib.Track{{Codec: &codecs.VP9{}}, rtmpAACTrack()}, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readRTMPPublisher(t, publishRTMPTracks(tc.tracks))
			require.ErrorContains(t, err, tc.error)
		})
	}
}

func TestRTMPReaderPreservesMultitrackRelay(t *testing.T) {
	for _, count := range []int{1, 2, 6} {
		t.Run(fmt.Sprintf("%d videos", count), func(t *testing.T) {
			tracks := make([]*gortmplib.Track, 0, count+1)
			for range count {
				tracks = append(tracks, rtmpH264Track())
			}
			tracks = append(tracks, rtmpAACTrack())
			session, err := readRTMPPublisher(t, publishRTMPTracks(tracks))
			require.NoError(t, err)
			conn := &recordingConn{}
			require.ErrorIs(t, relayRTMPSession(context.Background(), conn, session), io.EOF)
			frames := map[uint8][][2]time.Duration{}
			for _, msg := range conn.msgs {
				switch msg := msg.(type) {
				case *message.Video:
					if msg.PacketType == message.VideoPacketTypeAU {
						frames[0] = append(frames[0], [2]time.Duration{msg.DTS, msg.DTS + msg.PTSDelta})
					}
				case *message.VideoExMultitrack:
					switch packet := msg.Wrapped.(type) {
					case *message.VideoExCodedFrames:
						frames[msg.TrackID] = append(frames[msg.TrackID], [2]time.Duration{packet.DTS, packet.DTS + packet.PTSDelta})
					case *message.VideoExFramesX:
						frames[msg.TrackID] = append(frames[msg.TrackID], [2]time.Duration{packet.DTS, packet.DTS})
					}
				}
			}
			require.Len(t, frames, count, "every declared source video survives the relay")
			for _, stamps := range frames {
				require.Equal(t, [][2]time.Duration{{0, 33 * time.Millisecond}, {2 * time.Second, 2 * time.Second}}, stamps)
			}
		})
	}
}

func TestRTMPReaderAcceptsAV1SequenceStartOnlyConfig(t *testing.T) {
	// A real sequence header from gortmplib's AV1 wire fixture. It is present
	// only in the sequence-start message, never repeated in coded frames.
	config := &mp4.Av1C{
		Marker: 1, Version: 1, SeqLevelIdx0: 8, ChromaSubsamplingX: 1, ChromaSubsamplingY: 1,
		ConfigOBUs: []byte{0x0a, 0x0b, 0, 0, 0, 0x42, 0xab, 0xbf, 0xc3, 0x70, 0x0b, 0xe0, 1},
	}
	session, err := readRTMPPublisher(t, func(conn gortmplib.Conn) error {
		audio := rtmpAACTrack()
		writer := &gortmplib.Writer{Conn: conn, Tracks: []*gortmplib.Track{audio}}
		if err := writer.Initialize(); err != nil {
			return err
		}
		if err := conn.Write(&message.VideoExSequenceStart{
			ChunkStreamID: 6, MessageStreamID: 1, FourCC: message.FourCCAV1, AV1Config: config,
		}); err != nil {
			return err
		}
		for _, stamp := range []time.Duration{0, 2 * time.Second} {
			if err := conn.Write(&message.VideoExCodedFrames{
				ChunkStreamID: 6, MessageStreamID: 1, FourCC: message.FourCCAV1,
				DTS: stamp, Payload: []byte{0x12, 0},
			}); err != nil {
				return err
			}
			if err := writer.WriteMPEG4Audio(audio, stamp, []byte{0x21, 0x10, 0x04, 0x60, 0x8c, 0x1c}); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.Len(t, session.VideoTracks, 1)
	require.IsType(t, &codecs.AV1{}, session.VideoTracks[0].Track.Codec)
	var expected bytes.Buffer
	_, err = mp4.Marshal(&expected, config, mp4.Context{})
	require.NoError(t, err)
	require.Equal(t, expected.Bytes(), session.VideoTracks[0].AV1Config)
	var frames []*media.RTMPVideoData
	for event := range session.EventChan {
		if frame, ok := event.(*media.RTMPVideoData); ok {
			frames = append(frames, frame)
		}
	}
	require.Len(t, frames, 2)
	for i, frame := range frames {
		require.Zero(t, frame.TrackID)
		require.Equal(t, time.Duration(i)*2*time.Second, frame.DTS)
		require.Equal(t, frame.DTS, frame.PTS)
		encoded, err := av1.Bitstream(frame.AU).Marshal()
		require.NoError(t, err)
		require.Equal(t, []byte{0x12, 0}, encoded)
	}
}
