package media

import (
	"sync"

	"stream.place/streamplace/pkg/spmetrics"
)

// Relay totals replace player leases, which remain warm for a relay outage.
// nil relay means unavailable; an empty map is an authoritative zero.
type moqViewerCounts struct {
	mu      sync.Mutex
	players map[string]int
	relay   map[string]int
}

func (mm *MediaManager) initMoqViewers() {
	mm.moqViewers.players = make(map[string]int)
	mm.moqSessions = newPlaybackSessionTracker(playbackSessionTTL,
		func(did string) {
			mm.addMoqPlayer(did, 1)
			if rec := mm.viewRecorder; rec != nil {
				go rec(did)
			}
		},
		func(did string) { mm.addMoqPlayer(did, -1) },
	)
}

func (mm *MediaManager) TouchMoqSession(did, sid string) { mm.moqSessions.Touch(did, sid) }
func (mm *MediaManager) CloseMoqSession(did, sid string) { mm.moqSessions.Close(did, sid) }

func (mm *MediaManager) addMoqPlayer(did string, delta int) {
	v := &mm.moqViewers
	v.mu.Lock()
	defer v.mu.Unlock()
	v.players[did] += delta
	if v.players[did] == 0 {
		delete(v.players, did)
	}
	if v.relay == nil {
		mm.addMoqViewers(did, delta)
	}
}

func (mm *MediaManager) addMoqViewers(did string, delta int) {
	if delta == 0 {
		return
	}
	mm.bus.AddViewerCount(did, "local", delta)
	spmetrics.ViewerAdd(did, "moq", delta)
}

// SetMoqRelayViewers takes ownership of a complete snapshot for this node's
// playback namespace. The caller must exclude relay-to-relay subscriptions.
func (mm *MediaManager) SetMoqRelayViewers(counts map[string]int) {
	v := &mm.moqViewers
	v.mu.Lock()
	defer v.mu.Unlock()
	before := v.relay
	if before == nil {
		before = v.players
	}
	after := counts
	if after == nil {
		after = v.players
	}
	for did, count := range before {
		mm.addMoqViewers(did, after[did]-count)
	}
	for did, count := range after {
		if _, ok := before[did]; !ok {
			mm.addMoqViewers(did, count)
		}
	}
	v.relay = counts
}
