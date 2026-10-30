// Package main contains an example.
package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/abema/go-mp4"
	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/codecs"
	"github.com/bluenviron/gortmplib/pkg/message"
	"golang.org/x/sync/errgroup"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/log"
	"stream.place/streamplace/pkg/media"
)

// This example shows how to:
// 1. create a RTMP server
// 2. accept a stream from a reader.
// 3. broadcast the stream to readers.

var RTMPTimeout = 10 * time.Second

const RTMPPrefix = "/live/"

func (a *StreamplaceAPI) HandleRTMPPublisher(ctx context.Context, sc *gortmplib.ServerConn) error {
	err := sc.RW.(net.Conn).SetReadDeadline(time.Now().Add(RTMPTimeout))
	if err != nil {
		return err
	}

	if !strings.HasPrefix(sc.URL.Path, RTMPPrefix) {
		return fmt.Errorf("RTMP publisher is not allowed to publish to %s (must start with %s)", sc.URL.String(), RTMPPrefix)
	}
	streamKey := strings.TrimPrefix(sc.URL.Path, RTMPPrefix)
	mediaSigner, err := a.MakeMediaSigner(ctx, streamKey)
	if err != nil {
		return fmt.Errorf("failed to make media signer: %w", err)
	}

	streamer := mediaSigner.Streamer()
	ctx = log.WithLogValues(ctx, "streamer", streamer)
	session := &media.RTMPSession{
		EventChan:   make(chan any, 1024),
		MediaSigner: mediaSigner,
	}
	g, ctx := errgroup.WithContext(ctx)
	r, err := initializeRTMPReader(ctx, sc, session)
	if err != nil {
		return err
	}

	stopRead := context.AfterFunc(ctx, func() { _ = sc.RW.(net.Conn).Close() })
	defer stopRead()
	g.Go(func() error {
		defer close(session.EventChan)
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			err = sc.RW.(net.Conn).SetReadDeadline(time.Now().Add(RTMPTimeout))
			if err != nil {
				return err
			}
			err = r.Read()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil // close the packet input and let native mux/sign drain
				}
				return err
			}
		}
	})

	g.Go(func() error {
		return a.MediaManager.RTMPIngest(ctx, session)
	})

	return g.Wait()
}

// initializeRTMPReader is shared by live publishers and the unpublished shadow.
func initializeRTMPReader(ctx context.Context, sc *gortmplib.ServerConn, session *media.RTMPSession) (*gortmplib.Reader, error) {
	r := &gortmplib.Reader{Conn: sc}
	if err := r.Initialize(); err != nil {
		return nil, err
	}
	send := func(event any) {
		select {
		case session.EventChan <- event:
		case <-ctx.Done():
		}
	}
	// Observe the reader's rewound connection so configuration and DTS are
	// available for replayed packets too. The AV1 callback supplies only PTS and
	// does not expose the sequence-start decoder configuration.
	videoConfigs := map[uint8][]byte{}
	var lastDTS time.Duration
	configured := false
	r.Conn = &rtmpObservedConn{Conn: r.Conn, onRead: func(msg message.Message) error {
		var id uint8
		if multi, ok := msg.(*message.VideoExMultitrack); ok {
			id, msg = multi.TrackID, multi.Wrapped
		}
		switch packet := msg.(type) {
		case *message.Video:
			lastDTS = packet.DTS
			if !configured && packet.PacketType == message.VideoPacketTypeConfig {
				videoConfigs[id] = nil
			}
		case *message.VideoExCodedFrames:
			lastDTS = packet.DTS
		case *message.VideoExFramesX:
			lastDTS = packet.DTS
		case *message.VideoExSequenceStart:
			if !configured {
				var config bytes.Buffer
				if packet.FourCC == message.FourCCAV1 {
					if _, err := mp4.Marshal(&config, packet.AV1Config, mp4.Context{}); err != nil {
						return fmt.Errorf("AV1 configuration: %w", err)
					}
				}
				videoConfigs[id] = config.Bytes()
			}
		}
		return nil
	}}
	for _, track := range r.Tracks() {
		switch track.Codec.(type) {
		case *codecs.H264:
			id := len(session.VideoTracks)
			session.VideoTracks = append(session.VideoTracks, media.RTMPVideoTrack{Track: track})
			r.OnDataH264(track, func(pts, dts time.Duration, au [][]byte) {
				send(&media.RTMPVideoData{TrackID: id, AU: au, PTS: pts, DTS: dts})
			})
		case *codecs.AV1:
			id := len(session.VideoTracks)
			session.VideoTracks = append(session.VideoTracks, media.RTMPVideoTrack{Track: track})
			r.OnDataAV1(track, func(pts time.Duration, tu [][]byte) {
				send(&media.RTMPVideoData{TrackID: id, AU: tu, PTS: pts, DTS: lastDTS})
			})
		case *codecs.MPEG4Audio:
			if session.AudioTrack != nil {
				return nil, fmt.Errorf("RTMP supports one AAC audio track")
			}
			session.AudioTrack = track
			r.OnDataMPEG4Audio(track, func(pts time.Duration, au []byte) {
				send(&media.RTMPAACData{AU: au, PTS: pts})
			})
		default:
			return nil, fmt.Errorf("unsupported track codec: %T", track.Codec)
		}
	}
	if len(session.VideoTracks) == 0 || len(session.VideoTracks) > media.MaxRTMPVideoTracks {
		return nil, fmt.Errorf("RTMP requires 1 to %d video tracks", media.MaxRTMPVideoTracks)
	}
	if session.AudioTrack == nil {
		return nil, fmt.Errorf("RTMP requires one AAC audio track")
	}
	// All sequence-start messages are already in the reader's discovery replay.
	// Consume that prefix before constructing native branches, retaining any
	// interleaved media in the session's bounded channel.
	for len(videoConfigs) < len(session.VideoTracks) {
		if err := r.Read(); err != nil {
			return nil, fmt.Errorf("RTMP video configuration: %w", err)
		}
	}
	ids := make([]int, 0, len(videoConfigs))
	for id := range videoConfigs {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	for i, id := range ids {
		session.VideoTracks[i].AV1Config = videoConfigs[uint8(id)]
	}
	configured = true
	return r, nil
}

type rtmpObservedConn struct {
	gortmplib.Conn
	onRead func(message.Message) error
}

func (c *rtmpObservedConn) Read() (message.Message, error) {
	msg, err := c.Conn.Read()
	if err == nil {
		err = c.onRead(msg)
	}
	return msg, err
}

func (a *StreamplaceAPI) HandleRTMPPlayback(ctx context.Context, sc *gortmplib.ServerConn) error {
	if !strings.HasPrefix(sc.URL.Path, RTMPPrefix) {
		return fmt.Errorf("RTMP publisher is not allowed to publish to %s (must start with %s)", sc.URL.String(), RTMPPrefix)
	}
	streamer := strings.TrimPrefix(sc.URL.Path, RTMPPrefix)
	a.rtmpSessionsLock.Lock()
	session, ok := a.rtmpSessions[streamer]
	a.rtmpSessionsLock.Unlock()
	if !ok {
		return fmt.Errorf("RTMP session not found for streamer %s", streamer)
	}

	return relayRTMPSession(ctx, sc, session)
}

// relayRTMPSession writes a publisher's session out to conn, the internal
// playback connection the ingest pipeline reads with rtmp2src, until the
// session closes or ctx ends.
func relayRTMPSession(ctx context.Context, conn gortmplib.Conn, session *media.RTMPSession) error {
	tracks := []*gortmplib.Track{}
	for _, video := range session.VideoTracks {
		if _, ok := video.Track.Codec.(*codecs.H264); !ok {
			return fmt.Errorf("legacy RTMP relay supports H264 video only")
		}
		tracks = append(tracks, video.Track)
	}
	if session.AudioTrack != nil {
		tracks = append(tracks, session.AudioTrack)
	}
	w := &gortmplib.Writer{
		Conn:   conn,
		Tracks: tracks,
	}
	err := w.Initialize()
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-session.EventChan:
			if event == nil {
				return fmt.Errorf("RTMP session closed: %w", io.EOF)
			}
			switch event := event.(type) {
			case *media.RTMPVideoData:
				if event.TrackID < 0 || event.TrackID >= len(session.VideoTracks) {
					return fmt.Errorf("unknown RTMP video track %d", event.TrackID)
				}
				// gortmplib's reader hands on an AVC sequence header as an AU
				// of just SPS and PPS, including the stream's first one, which
				// it replays after reading ahead for the tracks. Written back as
				// a frame, it takes the timestamp of the keyframe after it, and
				// h264parse drops that keyframe's PTS, which fails the
				// segmenter's muxer. Write it as the sequence header it was.
				if msg, ok := h264ConfigMessage(event.AU, event.DTS); ok {
					var config message.Message = msg
					if event.TrackID != 0 {
						config = &message.VideoExMultitrack{TrackID: uint8(event.TrackID), Wrapped: &message.VideoExSequenceStart{
							ChunkStreamID: msg.ChunkStreamID, MessageStreamID: msg.MessageStreamID,
							FourCC: message.FourCCAVC, AVCConfig: msg.AVCConfig,
						}}
					}
					if err := conn.Write(config); err != nil {
						return fmt.Errorf("error writing H264 config: %w", err)
					}
					continue
				}
				err := w.WriteH264(session.VideoTracks[event.TrackID].Track, event.PTS, event.DTS, event.AU)
				if err != nil {
					return fmt.Errorf("error writing H264: %w", err)
				}
			case *media.RTMPAACData:
				err := w.WriteMPEG4Audio(session.AudioTrack, event.PTS, event.AU)
				if err != nil {
					return fmt.Errorf("error writing MPEG4Audio: %w", err)
				}
			default:
				return fmt.Errorf("unsupported event type: %T", event)
			}
		}
	}
}

func (a *StreamplaceAPI) HandleRTMPPublishConn(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	err := conn.SetReadDeadline(time.Now().Add(RTMPTimeout))
	if err != nil {
		return err
	}

	sc := &gortmplib.ServerConn{
		RW: conn,
	}
	err = sc.Initialize()
	if err != nil {
		return err
	}

	err = sc.AcceptConn()
	if err != nil {
		return err
	}

	if sc.Publish {
		return a.HandleRTMPPublisher(ctx, sc)
	}
	return fmt.Errorf("RTMP playback is not allowed")
}

func (a *StreamplaceAPI) HandleRTMPPlaybackConn(ctx context.Context, conn net.Conn) error {
	err := conn.SetReadDeadline(time.Now().Add(RTMPTimeout))
	if err != nil {
		return err
	}

	sc := &gortmplib.ServerConn{
		RW: conn,
	}
	err = sc.Initialize()
	if err != nil {
		return err
	}

	err = sc.AcceptConn()
	if err != nil {
		return err
	}

	if !sc.Publish {
		return a.HandleRTMPPlayback(ctx, sc)
	}
	return fmt.Errorf("RTMP playback is not allowed")
}

func (a *StreamplaceAPI) ServeRTMP(ctx context.Context) error {
	ln, err := net.Listen("tcp", a.CLI.RTMPAddr)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	defer ln.Close()

	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()

	log.Log(ctx, "rtmp server starting", "addr", a.CLI.RTMPAddr)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := ln.Accept()
		if err != nil {
			return fmt.Errorf("error accepting RTMP connection: %w", err)
		}
		go func() {
			err := a.HandleRTMPPublishConn(ctx, conn)
			if err != nil {
				log.Error(ctx, "error handling RTMP publish connection", "error", err)
			}
		}()
	}
}

func (a *StreamplaceAPI) ServeRTMPS(ctx context.Context, cli *config.CLI) error {
	var tlsConfig *tls.Config
	if a.ACME != nil {
		tlsConfig = a.ACME.TLSConfig()
	} else {
		cert, err := tls.LoadX509KeyPair(cli.TLSCertPath, cli.TLSKeyPath)
		if err != nil {
			return fmt.Errorf("failed to load TLS certificate: %w", err)
		}
		tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	}

	ln, err := tls.Listen("tcp", cli.RTMPSAddr, tlsConfig)
	if err != nil {
		return fmt.Errorf("failed to create RTMPS listener: %w", err)
	}
	defer ln.Close()

	log.Log(ctx, "rtmps server starting", "addr", cli.RTMPAddr)

	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := ln.Accept()
		if err != nil {
			return fmt.Errorf("error accepting RTMP connection: %w", err)
		}
		go func() {
			err := a.HandleRTMPPublishConn(ctx, conn)
			if err != nil {
				log.Error(ctx, "error handling RTMP publish connection", "error", err)
			}
		}()
	}
}

// h264ConfigMessage returns the FLV sequence header for an H.264 access unit
// that holds nothing but parameter sets (one SPS and one PPS), or false for
// any other AU.
func h264ConfigMessage(au [][]byte, dts time.Duration) (*message.Video, bool) {
	var sps, pps []byte
	for _, nalu := range au {
		if len(nalu) == 0 {
			return nil, false
		}
		switch nalu[0] & 0x1f {
		case 7: // SPS
			if sps != nil {
				return nil, false
			}
			sps = nalu
		case 8: // PPS
			if pps != nil {
				return nil, false
			}
			pps = nalu
		default:
			return nil, false
		}
	}
	// profile_idc, constraint flags and level_idc follow the NAL header
	if len(sps) < 4 || pps == nil {
		return nil, false
	}
	avcc := &mp4.AVCDecoderConfiguration{
		ConfigurationVersion:       1,
		Profile:                    sps[1],
		ProfileCompatibility:       sps[2],
		Level:                      sps[3],
		Reserved:                   0b111111,
		LengthSizeMinusOne:         3,
		Reserved2:                  0b111,
		NumOfSequenceParameterSets: 1,
		SequenceParameterSets:      []mp4.AVCParameterSet{{Length: uint16(len(sps)), NALUnit: sps}},
		NumOfPictureParameterSets:  1,
		PictureParameterSets:       []mp4.AVCParameterSet{{Length: uint16(len(pps)), NALUnit: pps}},
	}
	avcc.SetType(mp4.BoxTypeAvcC())
	return &message.Video{
		ChunkStreamID:   message.VideoChunkStreamID,
		MessageStreamID: 0x1000000,
		Codec:           message.CodecH264,
		FrameType:       message.VideoFrameTypeKeyFrame,
		PacketType:      message.VideoPacketTypeConfig,
		AVCConfig:       avcc,
		DTS:             dts,
	}, true
}
