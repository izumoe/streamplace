package websocketrep

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	official "moq.dev/moq"
	"stream.place/streamplace/pkg/log"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/moq"
	"stream.place/streamplace/pkg/muxl"
	"stream.place/streamplace/pkg/spmetrics"
)

// moqSourceChannel is the bus channel a stream's signed source segments
// ride; its rendition addenda ride media.RenditionsChannel. A broadcast
// carries both as one CMAF track per MUXL track (see docs/moq-for-devs.md).
const moqSourceChannel = "source"

const moqFullSourcePrefix = "source/"

// Subscriber priorities: under contention the source tracks, which carry
// the audio, go before the renditions.
const (
	moqSourcePriority    = uint8(200)
	moqRenditionPriority = uint8(150)
)

// moqMaxAgeUs is how long a group that is no longer the newest stays
// deliverable to a subscriber: one that falls behind by more skips ahead
// rather than validating stale segments.
const moqMaxAgeUs = uint64(10 * time.Second / time.Microsecond)

// moqLivenessInterval is how often a served broadcast checks that its
// stream is still live on the bus.
var moqLivenessInterval = time.Second

// moqTLSRetry is how often a listener without a certificate yet, a new
// node's ACME one still on its way, asks for it again.
var moqTLSRetry = 5 * time.Second

// serveMoQ shares the same on-demand broadcasts between the local listener
// and an outbound relay. Neither path re-encodes or re-signs media.
func (r *WebsocketReplicator) serveMoQ(ctx context.Context) error {
	origin := official.NewOriginProducer()
	requests, err := origin.Dynamic("", official.Route{})
	if err != nil {
		return err
	}
	defer requests.Cancel()
	g, gctx := errgroup.WithContext(ctx)
	if r.cli.MoqAddr != "" {
		g.Go(func() error { return r.listenMoQ(gctx, origin) })
	}
	if len(r.cli.MoqStatsURLs) > 0 {
		g.Go(func() error { return r.runMoqViewerStats(gctx) })
	}
	if r.cli.MoqRelayURL != "" {
		g.Go(func() error { return r.publishMoQRelay(gctx, origin) })
	}
	g.Go(func() error {
		var wg sync.WaitGroup
		defer wg.Wait()
		for req, err := range requests.Requests(gctx) {
			if err != nil {
				return err
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				r.serveBroadcast(gctx, req)
			}()
		}
		return nil
	})
	return g.Wait()
}

func (r *WebsocketReplicator) listenMoQ(ctx context.Context, origin *official.OriginProducer) error {
	if r.MoqTLS == nil {
		return fmt.Errorf("moq listener has no TLS files")
	}
	for {
		err := r.MoqTLS.Sync()
		if err == nil {
			break
		}
		log.Warn(ctx, "moq listener is waiting for its TLS certificate", "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(moqTLSRetry):
		}
	}
	// Go's ":port" means every interface, which the transport only takes
	// spelled out; its "[::]" socket is dual-stack.
	bind := r.cli.MoqAddr
	if host, port, err := net.SplitHostPort(bind); err == nil && host == "" {
		bind = net.JoinHostPort("::", port)
	}
	srv, err := official.Listen(ctx, bind,
		official.WithTLSCert(r.MoqTLS.CertPath), official.WithTLSKey(r.MoqTLS.KeyPath),
		official.WithServerPublishOrigin(origin))
	if err != nil {
		return fmt.Errorf("listening for moq: %w", err)
	}
	defer srv.Close()
	log.Log(ctx, "moq server starting", "addr", srv.LocalAddr())
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		r.MoqTLS.Refresh(gctx, func(err error) { log.Error(gctx, "refreshing moq TLS", "error", err) })
		return nil
	})
	g.Go(func() error { return srv.Serve(gctx) })
	return g.Wait()
}

// publishMoQRelay keeps the outbound route available across relay restarts.
// Failed initial connections use the same bounded backoff as origin pulls.
func (r *WebsocketReplicator) publishMoQRelay(ctx context.Context, origin *official.OriginProducer) error {
	delay := pullBackoffMin
	for {
		client, err := r.dialMoq(ctx, r.cli.MoqRelayURL,
			official.WithPublishOrigin(origin),
			official.WithReconnect(true),
			official.WithBackoff(official.Backoff{Timeout: official.RetryForever}),
		)
		if err == nil {
			log.Log(ctx, "moq relay connected")
			delay = pullBackoffMin
			err = client.Session().Closed(ctx)
			_ = client.Close()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Warn(ctx, "moq relay disconnected; retrying", "error", err, "backoff", delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, pullBackoffMax)
	}
}

// serveBroadcast answers a request for a streamer's broadcast: a
// stream that is not live here is refused rather than waited on, and a
// live one is served until it ends on the bus.
func (r *WebsocketReplicator) serveBroadcast(ctx context.Context, req *official.BroadcastRequest) {
	streamer, err := req.Path()
	if err != nil {
		return
	}
	sourceChannel := moqSourceChannel
	h264Only := true
	if strings.HasPrefix(streamer, "playback/") {
		streamer = strings.TrimPrefix(streamer, "playback/")
		sourceChannel = media.PlaybackSourceRendition
		h264Only = false
	} else if strings.HasPrefix(streamer, moqFullSourcePrefix) {
		streamer = strings.TrimPrefix(streamer, moqFullSourcePrefix)
		h264Only = false
	}
	if r.cli.DisableSyndication || !r.bus.HasSegments(streamer, sourceChannel) {
		_ = req.Reject(404)
		return
	}
	broadcast, err := official.NewBroadcastProducer()
	if err != nil {
		_ = req.Reject(500)
		return
	}
	defer broadcast.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		ticker := time.NewTicker(moqLivenessInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !r.bus.HasSegments(streamer, sourceChannel) {
					cancel()
					return
				}
			}
		}
	}()
	accepted := false
	r.serveTracks(ctx, streamer, sourceChannel, broadcast, h264Only, func() error {
		if err := req.Accept(broadcast); err != nil {
			return err
		}
		accepted = true
		return nil
	})
	if !accepted {
		_ = req.Reject(500)
	}
}

// serveTracks feeds broadcast from the streamer's bus until ctx ends or a
// segment can't be served. Source tracks are selected for the requested mode;
// rendition addenda keep their tracks. The CMAF broadcast is primed
// with the bus's recent segments like the websocket path. publish makes the
// broadcast reachable once the first source segment's tracks are all in the
// catalog: it grows a track at a time, and a subscriber that saw part of
// the set would reassemble partial segments.
func (r *WebsocketReplicator) serveTracks(ctx context.Context, streamer, sourceChannel string, broadcast *official.BroadcastProducer, h264Only bool, publish func() error) {
	source := r.bus.SubscribeSegmentBuf(ctx, streamer, sourceChannel, 2)
	defer r.bus.UnsubscribeSegment(context.Background(), streamer, sourceChannel, source)
	renditions := r.bus.SubscribeSegmentBuf(ctx, streamer, media.RenditionsChannel, 4)
	defer r.bus.UnsubscribeSegment(context.Background(), streamer, media.RenditionsChannel, renditions)
	tracks := moqTracks{}
	defer tracks.finish()
	// The tracks of one source segment share a group sequence, which is
	// how a peer puts the segment back together (see consumeSource).
	var sequence uint64
	var batchDuration time.Duration
	var sourceID string
	for {
		var err error
		select {
		case <-ctx.Done():
			return
		case seg := <-source.C:
			if seg.AudioOnly {
				if h264Only {
					continue // legacy peers complete audio with their own signer
				}
				if !seg.Published || sequence == 0 || seg.Filepath == "" || seg.Filepath != sourceID {
					continue
				}
				if sourceChannel == media.PlaybackSourceRendition {
					err = tracks.publishPlayback(ctx, broadcast, seg.Muxl)
				} else {
					err = tracks.publishAudioAddendum(ctx, broadcast, seg.Muxl)
				}
				if err != nil {
					log.Warn(ctx, "moq: could not publish audio continuation", "error", err)
					return
				}
				continue
			}
			if !seg.Published {
				if sourceChannel == media.PlaybackSourceRendition {
					if err := tracks.finishGroups(); err != nil {
						log.Warn(ctx, "moq: could not finish playback groups", "streamer", streamer, "error", err)
						return
					}
				}
				continue
			}
			// CMAF passthrough has no native handoff-jitter measurement. Tell
			// browser playback how much signed media arrives in one burst,
			// before any catalog or media from that batch becomes reachable.
			if sourceChannel == media.PlaybackSourceRendition && seg.BatchDuration > batchDuration {
				batchDuration = seg.BatchDuration
				milliseconds := batchDuration / time.Millisecond
				if batchDuration%time.Millisecond != 0 {
					milliseconds++
				}
				if err := broadcast.SetCatalogSection("streamplace", fmt.Sprintf(`{"batchDurationMs":%d}`, milliseconds)); err != nil {
					log.Warn(ctx, "moq: could not publish playback timing", "streamer", streamer, "error", err)
					return
				}
			}
			sourceID = seg.Filepath
			n := sequence
			sequence++
			if sourceChannel == media.PlaybackSourceRendition {
				err = tracks.publishPlayback(ctx, broadcast, seg.Muxl)
			} else {
				err = tracks.publish(ctx, broadcast, seg.Muxl, h264Only, func(t *official.CMAFProducer) (*official.CMAFGroupProducer, error) { return t.CreateGroup(n) })
			}
		case seg := <-renditions.C:
			if !seg.Published {
				continue
			}
			if sourceChannel == media.PlaybackSourceRendition {
				err = tracks.publishPlayback(ctx, broadcast, seg.Muxl)
			} else {
				err = tracks.publish(ctx, broadcast, seg.Muxl, false, (*official.CMAFProducer).AppendGroup)
			}
		}
		if err == nil && publish != nil && sequence > 0 {
			if err = publish(); err != nil {
				err = fmt.Errorf("could not publish broadcast: %w", err)
			}
			publish = nil
		}
		if err != nil {
			log.Warn(ctx, "moq: ending the broadcast", "streamer", streamer, "error", err)
			return
		}
	}
}

// moqTrack is one MUXL track served as a CMAF track. Its catalog entry
// comes from the track's own init, so a segment whose MUXL catalog box (a
// function of the codec config alone) differs cannot ride it.
type moqTrack struct {
	*official.CMAFProducer
	catalog []byte
	group   *official.CMAFGroupProducer
}

// moqTracks are a broadcast's tracks, by MUXL rendition name.
type moqTracks map[string]*moqTrack

func (ts moqTracks) finish() {
	_ = ts.finishGroups()
	for _, t := range ts {
		_ = t.Finish()
	}
}

// A publication gap ends the current GoPs. Resumed video must wait for sync.
func (ts moqTracks) finishGroups() error {
	var result error
	for name, t := range ts {
		if t.group != nil {
			if err := t.group.Finish(); err != nil {
				result = errors.Join(result, fmt.Errorf("%s track: %w", name, err))
			}
			t.group = nil
		}
	}
	return result
}

// publish writes each track of seg, a bare canonical segment, as a new
// group of its CMAF track, publishing the track on first sight: one frame
// per moof+mdat, the track's leading boxes (its C2PA manifest and MUXL
// catalog) riding the first, so the group's frames concatenate back to
// the signed bytes.
func (ts moqTracks) publish(ctx context.Context, broadcast *official.BroadcastProducer, seg []byte, h264Only bool, group func(*official.CMAFProducer) (*official.CMAFGroupProducer, error)) error {
	var tracks map[string][]byte
	var renditions map[string][]byte
	var err error
	if h264Only {
		var videoID uint32
		for name := range ts {
			if strings.HasPrefix(name, "video") {
				id, parseErr := strconv.ParseUint(strings.TrimPrefix(name, "video"), 10, 32)
				if parseErr == nil && id < uint64(media.RenditionTrackID(0)) {
					videoID = uint32(id)
					break
				}
			}
		}
		tracks, renditions, err = media.H264ReplicationTracks(ctx, seg, videoID)
	} else {
		tracks, err = media.MuxlTracks(ctx, seg)
	}
	if err != nil {
		return fmt.Errorf("replication source tracks: %w", err)
	}
	// Keep legacy source names low even when the signed audio IDs follow
	// supplied video. Only alias a complete supplied set, never mix it into
	// an existing low-ID audio namespace from another producer.
	aliasAudio := true
	for name := range tracks {
		if strings.HasPrefix(name, "audio") && replicationAudioName(name) == name {
			aliasAudio = false
		}
	}
	for name, data := range tracks {
		if aliasAudio {
			name = replicationAudioName(name)
		}
		if err := ts.publishTrack(ctx, broadcast, name, data, group); err != nil {
			return err
		}
	}
	for name, data := range renditions {
		if err := ts.publishTrack(ctx, broadcast, name, data, (*official.CMAFProducer).AppendGroup); err != nil {
			return err
		}
	}
	return nil
}

// Audio continuations use the existing rendition transport namespace. The
// canonical track id, codec header and signatures remain unchanged. Older
// peers therefore fold these bytes into HLS instead of rejecting an audio-only
// source batch; upgraded peers also deliver the tail to WebRTC and RTMP.
func (ts moqTracks) publishAudioAddendum(ctx context.Context, broadcast *official.BroadcastProducer, data []byte) error {
	tracks, err := media.MuxlTracks(ctx, data)
	if err != nil {
		return err
	}
	for name, data := range tracks {
		alias := replicationAudioName(name)
		if ts[alias] != nil && ts[name] == nil {
			name = alias
		}
		id, err := strconv.ParseUint(strings.TrimPrefix(name, "audio"), 10, 32)
		if err != nil || !strings.HasPrefix(name, "audio") || id >= uint64(media.RenditionTrackID(0)) {
			return fmt.Errorf("invalid continuation track %s", name)
		}
		rendition := fmt.Sprintf("audio%d", media.RenditionTrackID(int(id)))
		if err := ts.publishTrack(ctx, broadcast, rendition, data, (*official.CMAFProducer).AppendGroup); err != nil {
			return err
		}
	}
	return nil
}

// The wire name controls source/addendum routing on old peers. The CMAF init
// and media retain their actual track IDs and original signatures.
func replicationAudioName(name string) string {
	if !strings.HasPrefix(name, "audio") {
		return name
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(name, "audio"), 10, 32)
	if err != nil || id <= media.SuppliedRenditionTrackBase || id >= media.SuppliedRenditionTrackBase+uint64(media.RenditionTrackID(0)) {
		return name
	}
	return fmt.Sprintf("audio%d", id-media.SuppliedRenditionTrackBase)
}

func (ts moqTracks) publishTrack(ctx context.Context, broadcast *official.BroadcastProducer, name string, data []byte, group func(*official.CMAFProducer) (*official.CMAFGroupProducer, error)) error {
	track, fragments, err := ts.track(ctx, broadcast, name, data, nil)
	if err != nil {
		return err
	}
	g, err := group(track.CMAFProducer)
	if err != nil {
		return fmt.Errorf("track %s group: %w", name, err)
	}
	for _, fragment := range fragments {
		if err := g.WriteFragment(fragment); err != nil {
			return fmt.Errorf("track %s: %w", name, err)
		}
	}
	if err := g.Finish(); err != nil {
		return fmt.Errorf("track %s: %w", name, err)
	}
	return nil
}

// publishPlayback keeps each video's GoP open across admission batches.
// Browser groups must begin at a real sync sample; peer groups instead keep
// their shared admission sequence so consumeSource can reassemble them.
func (ts moqTracks) publishPlayback(ctx context.Context, broadcast *official.BroadcastProducer, seg []byte) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan *muxl.MuxlEvent, 16)
	done := make(chan error, 1)
	go func() {
		err := muxl.RunMuxlUnwrapEvents(ctx, bytes.NewReader(seg), events)
		close(events)
		done <- err
	}()
	var init *muxl.MuxlEvent
	var publishErr error
	for event := range events {
		if publishErr != nil {
			continue
		}
		if event.Type == "init" {
			init = event
			continue
		}
		write := func(name string, id uint32, video bool) error {
			key := strconv.FormatUint(uint64(id), 10)
			data := event.Tracks[key]
			if len(data) == 0 {
				return nil
			}
			t, fragments, err := ts.track(ctx, broadcast, name, data, init.TrackInits[key])
			if err != nil {
				return err
			}
			samples := event.Samples[key]
			if len(fragments) != len(samples.Sizes) {
				return fmt.Errorf("track %s has %d fragments for %d samples", name, len(fragments), len(samples.Sizes))
			}
			for i, fragment := range fragments {
				sync := video && slices.Contains(samples.SyncIndices, uint32(i+1))
				if sync && t.group != nil {
					if err := t.group.Finish(); err != nil {
						return fmt.Errorf("track %s group: %w", name, err)
					}
					t.group = nil
				}
				if t.group == nil {
					if video && !sync {
						continue
					}
					t.group, err = t.AppendGroup()
					if err != nil {
						return fmt.Errorf("track %s group: %w", name, err)
					}
				}
				if err := t.group.WriteFragment(fragment); err != nil {
					return fmt.Errorf("track %s: %w", name, err)
				}
			}
			return nil
		}
		if init == nil || init.Catalog == nil {
			publishErr = errors.New("segment has no MUXL catalog")
		} else {
			if init.Catalog.Video != nil {
				for name, config := range init.Catalog.Video.Renditions {
					if publishErr = write(name, config.TrackID(), true); publishErr != nil {
						break
					}
				}
			}
			if publishErr == nil && init.Catalog.Audio != nil {
				for name, config := range init.Catalog.Audio.Renditions {
					if publishErr = write(name, config.TrackID(), false); publishErr != nil {
						break
					}
				}
			}
		}
		if publishErr != nil {
			cancel()
		}
	}
	if err := <-done; err != nil && publishErr == nil {
		publishErr = err
	}
	if publishErr != nil {
		return publishErr
	}
	if init != nil && init.Catalog != nil && init.Catalog.Audio != nil {
		for name := range init.Catalog.Audio.Renditions {
			if t := ts[name]; t != nil && t.group != nil {
				if err := t.group.Finish(); err != nil {
					return fmt.Errorf("track %s group: %w", name, err)
				}
				t.group = nil
			}
		}
	}
	return nil
}

func (ts moqTracks) track(ctx context.Context, broadcast *official.BroadcastProducer, name string, data, init []byte) (*moqTrack, [][]byte, error) {
	fragments, catalog, err := cmafFragments(data)
	if err != nil {
		return nil, nil, fmt.Errorf("track %s: %w", name, err)
	}
	t := ts[name]
	if t != nil && !bytes.Equal(t.catalog, catalog) {
		return nil, nil, fmt.Errorf("track %s changed its codec config", name)
	}
	if t == nil {
		if len(init) == 0 {
			var buf bytes.Buffer
			if err := muxl.RunMuxlWrapInit(ctx, bytes.NewReader(data), &buf); err != nil {
				return nil, nil, fmt.Errorf("track %s init: %w", name, err)
			}
			init = buf.Bytes()
		}
		producer, err := broadcast.PublishCMAF(name, init)
		if err != nil {
			return nil, nil, fmt.Errorf("publishing track %s: %w", name, err)
		}
		t = &moqTrack{CMAFProducer: producer, catalog: catalog}
		ts[name] = t
	}
	return t, fragments, nil
}

// cmafFragments splits one track's canonical segment into its CMAF
// fragments, one per moof+mdat with the leading boxes riding the first,
// and returns its MUXL catalog box: the box right before the first moof.
func cmafFragments(track []byte) (fragments [][]byte, catalog []byte, err error) {
	start, prev := 0, -1
	for off := 0; off < len(track); {
		if len(track)-off < 8 {
			return nil, nil, errors.New("truncated box")
		}
		size := int(binary.BigEndian.Uint32(track[off:]))
		if size < 8 || size > len(track)-off {
			return nil, nil, fmt.Errorf("box size %d out of range", size)
		}
		switch string(track[off+4 : off+8]) {
		case "moof":
			if catalog == nil && prev >= 0 {
				catalog = track[prev:off]
			}
		case "mdat":
			fragments = append(fragments, track[start:off+size])
			start = off + size
		}
		prev = off
		off += size
	}
	if catalog == nil || start != len(track) {
		return nil, nil, errors.New("not a canonical MUXL track")
	}
	return fragments, catalog, nil
}

// dialMoq opens a session to a moqt:// URL, pinning the certificate its
// certhash parameter names when it has one.
func (r *WebsocketReplicator) dialMoq(ctx context.Context, rawURL string, options ...official.ClientOption) (*official.Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("could not parse moq URL: %w", err)
	}
	opts := []official.ClientOption{
		official.WithPublishOrigin(official.NewOriginProducer()),
		official.WithSubscribeOrigin(official.NewOriginProducer()),
		// The pull and push loops own reconnects, so each attempt
		// rediscovers the URL and its certificate hash.
		official.WithReconnect(false),
		official.WithWebSocketEnabled(false),
	}
	if q := u.Query(); q.Has(moq.CertHashParam) {
		pin := q.Get(moq.CertHashParam)
		if decoded, err := hex.DecodeString(pin); err != nil || len(decoded) != 32 {
			return nil, fmt.Errorf("invalid moq certificate SHA-256 fingerprint")
		}
		opts = append(opts, official.WithTLSFingerprints(pin))
		q.Del(moq.CertHashParam)
		u.RawQuery = q.Encode()
	}
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client, err := official.Dial(dialCtx, u.String(), append(opts, options...)...)
	if err != nil {
		spmetrics.ReplicationConnectErrorsTotal.Inc()
		return nil, fmt.Errorf("%w (%s): %w", errMoqUnreachable, rawURL, err)
	}
	return client, nil
}

// requestMoqBroadcast resolves streamer's broadcast on a session to a peer.
// The peer's route arrives just after the session opens, so this waits for
// it, but not forever; the peer then serves the broadcast or refuses it.
func requestMoqBroadcast(ctx context.Context, client *official.Client, streamer string) (*official.BroadcastConsumer, error) {
	announced, err := client.AnnouncedBroadcast(streamer)
	if err != nil {
		return nil, err
	}
	defer announced.Cancel()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return announced.Available(ctx)
}

// requestReplicationBroadcast uses the full source when a peer supports it.
// Only an explicit missing-broadcast response permits the H264 fallback.
func requestReplicationBroadcast(ctx context.Context, client *official.Client, streamer string) (*official.BroadcastConsumer, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var lastErr error
	for _, path := range [...]string{moqFullSourcePrefix + streamer, streamer} {
		broadcast, err := requestMoqBroadcast(ctx, client, path)
		if err == nil {
			_, err = broadcast.Catalog(ctx)
			if err == nil {
				return broadcast, nil
			}
		}
		protocol, ok := official.ProtocolError(err)
		// MoQ encodes stream application errors at wire code 64 + application code.
		if !ok || protocol.Scope != official.ErrorScopeStream || protocol.Kind != official.ProtocolKindApp || protocol.Code != 64+404 {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// consume follows one streamer's broadcast catalog and feeds its tracks
// through validation until the broadcast or ctx ends; it always returns
// an error saying why. Anything that fails validation ends it: the wire
// carries MUXL and nothing else.
func (r *WebsocketReplicator) consume(ctx context.Context, broadcast *official.BroadcastConsumer, streamer string) error {
	spmetrics.ReplicationOutboundOpen.WithLabelValues(streamer).Inc()
	defer spmetrics.ReplicationOutboundOpen.WithLabelValues(streamer).Dec()
	return consumeBroadcast(ctx, broadcast,
		func(segment []byte) error {
			if err := r.mm.ValidateReplicatedMP4(context.Background(), bytes.NewReader(segment), streamer); err != nil {
				return fmt.Errorf("could not validate segment: %w", err)
			}
			return nil
		},
		func(ctx context.Context, addendum []byte) error {
			err := r.feedRenditions(ctx, streamer, addendum)
			if !errors.Is(err, media.ErrAudioSourceMissing) {
				return err
			}
			source := r.bus.SubscribeSegment(ctx, streamer, "source")
			defer r.bus.UnsubscribeSegment(context.Background(), streamer, "source", source)
			waiting, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			for {
				// Retry after subscribing so an admission racing the subscription is
				// observed either in the canonical buffer or in the source channel.
				err = r.feedRenditions(waiting, streamer, addendum)
				if !errors.Is(err, media.ErrAudioSourceMissing) {
					return err
				}
				select {
				case <-source.C:
				case <-waiting.Done():
					return fmt.Errorf("audio continuation source: %w", waiting.Err())
				}
			}
		})
}

// consumeBroadcast hands each source segment the broadcast's catalog
// leads to, reassembled, to source, and each rendition track's segments to
// rendition, until something fails or the broadcast or ctx ends.
func consumeBroadcast(ctx context.Context, broadcast *official.BroadcastConsumer, source func([]byte) error, rendition func(context.Context, []byte) error) error {
	catalogs, err := broadcast.SubscribeCatalog(ctx)
	if err != nil {
		spmetrics.ReplicationConnectErrorsTotal.Inc()
		return fmt.Errorf("could not subscribe to the catalog: %w", err)
	}
	defer catalogs.Cancel()
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var sourceTracks []string
		renditions := map[string]bool{}
		for catalog, err := range catalogs.Updates(gctx) {
			if err != nil {
				return fmt.Errorf("catalog: %w", err)
			}
			src, ren := moqTrackSets(catalog)
			// The origin's first catalog already holds a source segment's
			// whole track set (see serveTracks); one that changes it
			// starts a new session, which reads the new set.
			if sourceTracks == nil && len(src) > 0 {
				sourceTracks = src
				g.Go(func() error { return consumeSource(gctx, broadcast, src, source) })
			} else {
				// Retired catalog entries still have subscribed media to drain.
				for _, name := range src {
					if !slices.Contains(sourceTracks, name) {
						return fmt.Errorf("source tracks changed from %v to %v", sourceTracks, src)
					}
				}
			}
			for _, name := range ren {
				if renditions[name] {
					continue
				}
				renditions[name] = true
				g.Go(func() error {
					return consumeTrack(gctx, broadcast, name, moqRenditionPriority, func(addendum []byte) error {
						return rendition(gctx, addendum)
					})
				})
			}
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		return err
	}
	return errors.New("broadcast ended")
}

// moqTrackSets sorts a catalog's tracks, named by their MUXL rendition
// ("video1", "audio2"), into the source segment's, in track-id order, and
// the renditions' (track ids from media.RenditionTrackID(0) on). A track
// named otherwise is not Streamplace's and is ignored.
func moqTrackSets(catalog *official.Catalog) (source, renditions []string) {
	ids := map[string]uint64{}
	add := func(name, kind string) {
		if id, err := strconv.ParseUint(strings.TrimPrefix(name, kind), 10, 32); err == nil && strings.HasPrefix(name, kind) {
			ids[name] = id
			if id < uint64(media.RenditionTrackID(0)) {
				source = append(source, name)
			} else {
				renditions = append(renditions, name)
			}
		}
	}
	for name := range catalog.Video {
		add(name, "video")
	}
	for name := range catalog.Audio {
		add(name, "audio")
	}
	slices.SortFunc(source, func(a, b string) int { return int(ids[a]) - int(ids[b]) })
	return source, renditions
}

// consumeSource reassembles source batches in sequence order, keeping tracks
// in track-id order. A track can end before the others without losing their
// remaining batches; only tracks with a matching group contribute bytes.
func consumeSource(ctx context.Context, broadcast *official.BroadcastConsumer, names []string, handle func([]byte) error) error {
	tracks := make([]*official.TrackConsumer, len(names))
	for i, name := range names {
		track, err := subscribeMoqTrack(ctx, broadcast, name, moqSourcePriority)
		if err != nil {
			return err
		}
		defer track.Cancel()
		tracks[i] = track
	}
	// The next unread group of each track.
	ahead := make([]*official.GroupConsumer, len(tracks))
	defer func() {
		for _, group := range ahead {
			if group != nil {
				group.Cancel()
			}
		}
	}()
	for {
		var sequence uint64
		found := false
		for i, track := range tracks {
			if track == nil {
				continue
			}
			if ahead[i] == nil {
				group, err := track.NextGroup(ctx)
				if err != nil {
					return fmt.Errorf("%s track: %w", names[i], err)
				}
				if group == nil {
					tracks[i] = nil
					continue
				}
				ahead[i] = group
			}
			if !found || ahead[i].Sequence() < sequence {
				sequence = ahead[i].Sequence()
				found = true
			}
		}
		if !found {
			return nil
		}
		var segment []byte
		for i, group := range ahead {
			if group == nil || group.Sequence() != sequence {
				continue
			}
			ahead[i] = nil
			part, err := readMoqGroup(ctx, group)
			if err != nil {
				return fmt.Errorf("%s track: %w", names[i], err)
			}
			segment = append(segment, part...)
		}
		if err := handle(segment); err != nil {
			return err
		}
	}
}

// consumeTrack hands each group of a track, one track's canonical segment,
// to handle in sequence order, until the track ends, handle fails or ctx
// ends.
func consumeTrack(ctx context.Context, broadcast *official.BroadcastConsumer, name string, priority uint8, handle func([]byte) error) error {
	track, err := subscribeMoqTrack(ctx, broadcast, name, priority)
	if err != nil {
		return err
	}
	defer track.Cancel()
	for {
		group, err := track.NextGroup(ctx)
		if err != nil {
			return fmt.Errorf("%s track: %w", name, err)
		}
		if group == nil {
			return nil
		}
		segment, err := readMoqGroup(ctx, group)
		if err != nil {
			return fmt.Errorf("%s track: %w", name, err)
		}
		if err := handle(segment); err != nil {
			return err
		}
	}
}

func subscribeMoqTrack(ctx context.Context, broadcast *official.BroadcastConsumer, name string, priority uint8) (*official.TrackConsumer, error) {
	track, err := broadcast.SubscribeTrack(ctx, name, &official.Subscription{Priority: priority, MaxAgeUs: moqMaxAgeUs})
	if err != nil {
		spmetrics.ReplicationConnectErrorsTotal.Inc()
		return nil, fmt.Errorf("could not subscribe to %s: %w", name, err)
	}
	return track, nil
}

// moqMaxGroupBytes bounds a group read from a peer, which nothing has
// checked yet: one track's segment for one GoP, a few megabytes at the
// highest bitrate Streamplace recommends.
var moqMaxGroupBytes = 64 << 20

// readMoqGroup reads a group's frames, the CMAF fragments of one track's
// canonical segment, back into that segment.
func readMoqGroup(ctx context.Context, group *official.GroupConsumer) ([]byte, error) {
	defer group.Cancel()
	var segment []byte
	for {
		frame, err := group.ReadFrame(ctx)
		if err != nil {
			return nil, err
		}
		if frame == nil {
			break
		}
		if len(segment)+len(frame.Payload) > moqMaxGroupBytes {
			return nil, fmt.Errorf("group %d is over %d bytes", group.Sequence(), moqMaxGroupBytes)
		}
		segment = append(segment, frame.Payload...)
	}
	if !isMuxl(segment) {
		return nil, fmt.Errorf("group %d is not a signed MUXL segment", group.Sequence())
	}
	return segment, nil
}

// isMuxl recognises a bare canonical segment: it leads with the c2pa or
// muxl uuid box, never with ftyp/moov (a presentation MP4) or anything else.
func isMuxl(b []byte) bool {
	return len(b) >= 8 && string(b[4:8]) == "uuid"
}

// errMoqUnreachable is a peer whose MoQ listener could not be dialled.
var errMoqUnreachable = errors.New("could not dial moq")
