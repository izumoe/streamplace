package websocketrep

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	official "moq.dev/moq"
	"stream.place/streamplace/pkg/placestream"
	"stream.place/streamplace/pkg/statedb"
)

// moqViewerSnapshot counts sessions, not tracks. Legacy counter spellings are
// still emitted by relays; a present canonical zero takes precedence.
func moqViewerSnapshot(data []byte, prefix string) (map[string]int, error) {
	var snapshot map[string]*struct {
		Started       *uint64 `json:"broadcasts_started"`
		Ended         *uint64 `json:"broadcasts_ended"`
		LegacyStarted uint64  `json:"broadcasts"`
		LegacyEnded   uint64  `json:"broadcasts_closed"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, fmt.Errorf("missing stats snapshot")
	}
	counts := make(map[string]int)
	for path, traffic := range snapshot {
		did, ok := strings.CutPrefix(path, prefix)
		if !ok || !strings.HasPrefix(did, "did:") || strings.Contains(did, "/") {
			continue
		}
		if traffic == nil {
			return nil, fmt.Errorf("missing traffic counters")
		}
		started, ended := traffic.LegacyStarted, traffic.LegacyEnded
		if traffic.Started != nil {
			started = *traffic.Started
		}
		if traffic.Ended != nil {
			ended = *traffic.Ended
		}
		if ended > started || started-ended > math.MaxInt {
			return nil, fmt.Errorf("invalid viewer counters")
		}
		counts[did] = int(started - ended)
	}
	return counts, nil
}

// watchMoqStats consumes a complete end-user snapshot. Unchanged snapshots do
// not produce frames: liveness belongs to the connection, not a frame timeout.
func (r *WebsocketReplicator) watchMoqStats(ctx context.Context, rawURL, track, prefix string, update func(map[string]int)) error {
	client, err := r.dialMoq(ctx, rawURL)
	if err != nil {
		return err
	}
	defer client.Close()
	setup, cancel := context.WithTimeout(ctx, 15*time.Second)
	broadcast, err := requestMoqBroadcast(setup, client, "")
	if err != nil {
		cancel()
		return err
	}
	consumer, err := broadcast.SubscribeJSONSnapshot(setup, track, official.JSONSubscribeOptions{})
	if err != nil {
		cancel()
		return err
	}
	defer consumer.Cancel()
	first, err := consumer.Next(setup)
	cancel()
	if err != nil {
		return err
	}
	if first == nil {
		return io.EOF
	}
	counts, err := moqViewerSnapshot(*first, prefix)
	if err != nil {
		return err
	}
	update(counts)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return client.Session().Closed(gctx) })
	g.Go(func() error {
		for {
			value, err := consumer.Next(gctx)
			if err != nil {
				return err
			}
			if value == nil {
				return io.EOF
			}
			counts, err := moqViewerSnapshot(*value, prefix)
			if err != nil {
				return err
			}
			update(counts)
		}
	})
	return g.Wait()
}

// runMoqViewerStats uses all configured relay nodes or none. A missing node
// must not silently replace the warm player fallback with a partial total.
func (r *WebsocketReplicator) runMoqViewerStats(ctx context.Context) error {
	type snapshot struct {
		node   int
		counts map[string]int
	}
	updates := make(chan snapshot)
	g, gctx := errgroup.WithContext(ctx)
	u, _ := url.Parse(r.cli.MoqRelayURL) // checked by config
	prefix := strings.Trim(u.Path, "/")
	if prefix != "" {
		prefix += "/"
	}
	prefix += "playback/"
	track := "publisher.json"
	if r.cli.MoqStatsTier != "" {
		track = r.cli.MoqStatsTier + "/" + track
	}
	for node, rawURL := range r.cli.MoqStatsURLs {
		g.Go(func() error {
			update := func(counts map[string]int) {
				select {
				case updates <- snapshot{node, counts}:
				case <-gctx.Done():
				}
			}
			for {
				// Errors may contain the private auth URL. Never put them in logs.
				_ = r.watchMoqStats(gctx, rawURL, track, prefix, update)
				update(nil)
				select {
				case <-gctx.Done():
					return gctx.Err()
				case <-time.After(10 * time.Second):
				}
			}
		})
	}
	g.Go(func() error {
		defer r.mm.SetMoqRelayViewers(nil)
		ticker := time.NewTicker(originPollInterval)
		defer ticker.Stop()
		nodes := make([]map[string]int, len(r.cli.MoqStatsURLs))
		var owners map[string]statedb.BroadcastOrigin
		refreshOwners := true
		myDID := r.cli.ServerDID()
		for {
			select {
			case <-gctx.Done():
				return gctx.Err()
			case next := <-updates:
				nodes[next.node] = next.counts
			case <-ticker.C:
				refreshOwners = true
			}
			counts := make(map[string]int)
			complete := true
			for _, values := range nodes {
				if values == nil {
					complete = false
					break
				}
				for did, count := range values {
					if count > math.MaxInt-counts[did] {
						complete = false
						break
					}
					counts[did] += count
				}
			}
			if !complete || r.state == nil {
				r.mm.SetMoqRelayViewers(nil)
				continue
			}
			// Ownership can change without a new stats frame. Only the current
			// origin reports a stream's relay totals.
			if refreshOwners {
				refreshOwners = false
				owners = nil
				origins, err := r.state.ListBroadcastOriginsSince(time.Now().Add(-originPollWindow))
				if err == nil {
					owners = make(map[string]statedb.BroadcastOrigin, len(origins))
					for _, origin := range origins {
						if _, known := owners[origin.StreamerRepoDID]; !known {
							owners[origin.StreamerRepoDID] = origin
						}
					}
				}
			}
			if owners == nil {
				r.mm.SetMoqRelayViewers(nil)
				continue
			}
			for did := range counts {
				origin := owners[did]
				if origin.ServerDID != myDID {
					delete(counts, did)
					continue
				}
				// Foreign announcements also arrive without a shared database;
				// their timestamps have whole-second precision.
				if latest, ok := r.latestFor(did); ok {
					foreign := latest.view.Record.Val.(*placestream.BroadcastOrigin)
					updated, err := time.Parse(time.RFC3339Nano, foreign.UpdatedAt)
					if err != nil || !updated.Before(origin.UpdatedAt.Truncate(time.Second)) {
						delete(counts, did)
						continue
					}
				}
				if _, published := r.mm.GetLiveWindow(did); !published {
					delete(counts, did)
				}
			}
			r.mm.SetMoqRelayViewers(counts)
		}
	})
	return g.Wait()
}
