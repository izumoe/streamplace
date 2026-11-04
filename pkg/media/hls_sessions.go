package media

import (
	"sync"
	"time"
)

// Playback leases survive brief stalls. HLS playlists renew about every two
// seconds; the MoQ player reports progress every ten seconds.
const playbackSessionTTL = 30 * time.Second
const playbackSessionSweepInterval = 5 * time.Second

type playbackSessionKey struct {
	streamer string
	sid      string
}

// playbackSessionTracker turns playback activity into viewer sessions: the
// first request carrying a given (streamer, sid) starts a session (onNew),
// and a session that goes idle for ttl ends it (onExpire). The two callbacks
// fire exactly once per session, in order, so they can safely drive an
// increment/decrement pair.
type playbackSessionTracker struct {
	ttl      time.Duration
	onNew    func(streamer string)
	onExpire func(streamer string)
	now      func() time.Time // clock, overridable in tests

	mu             sync.Mutex
	lastSeen       map[playbackSessionKey]time.Time
	janitorRunning bool
}

func newPlaybackSessionTracker(ttl time.Duration, onNew, onExpire func(streamer string)) *playbackSessionTracker {
	return &playbackSessionTracker{
		ttl:      ttl,
		onNew:    onNew,
		onExpire: onExpire,
		now:      time.Now,
		lastSeen: map[playbackSessionKey]time.Time{},
	}
}

// Touch records a request for (streamer, sid), starting a session if it's
// new. Empty sids are ignored — there's no session to attribute the request
// to.
func (t *playbackSessionTracker) Touch(streamer, sid string) {
	if streamer == "" || sid == "" {
		return
	}
	k := playbackSessionKey{streamer: streamer, sid: sid}
	t.mu.Lock()
	_, known := t.lastSeen[k]
	t.lastSeen[k] = t.now()
	startJanitor := false
	if !known && !t.janitorRunning {
		t.janitorRunning = true
		startJanitor = true
	}
	if !known {
		t.onNew(streamer)
	}
	t.mu.Unlock()
	if startJanitor {
		go t.janitor()
	}
}

// janitor sweeps periodically while any session exists, and exits once the
// tracker drains (the next Touch starts a fresh one).
func (t *playbackSessionTracker) janitor() {
	ticker := time.NewTicker(playbackSessionSweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		if t.sweep() {
			return
		}
	}
}

// sweep expires sessions idle past the ttl and reports whether the tracker is
// now empty (meaning the calling janitor should exit).
func (t *playbackSessionTracker) sweep() bool {
	cutoff := t.now().Add(-t.ttl)
	t.mu.Lock()
	var expired []string
	for k, last := range t.lastSeen {
		if last.Before(cutoff) {
			delete(t.lastSeen, k)
			expired = append(expired, k.streamer)
		}
	}
	empty := len(t.lastSeen) == 0
	if empty {
		t.janitorRunning = false
	}
	for _, streamer := range expired {
		t.onExpire(streamer)
	}
	t.mu.Unlock()
	return empty
}

// TouchHLSSession marks HLS playback activity for one (streamer, sid)
// session. Called by the live HLS handlers on media-playlist and segment
// requests; the first touch counts the viewer, going idle uncounts them.
func (mm *MediaManager) TouchHLSSession(streamer, sid string) {
	mm.hlsSessions.Touch(streamer, sid)
}

// Close removes a lease immediately when a player stops or changes transport.
func (t *playbackSessionTracker) Close(streamer, sid string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := playbackSessionKey{streamer: streamer, sid: sid}
	if _, ok := t.lastSeen[key]; ok {
		delete(t.lastSeen, key)
		t.onExpire(streamer)
	}
}
