// Package accessctl installs persisted node policy and publishes local changes.
package accessctl

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluesky-social/indigo/atproto/syntax"
	"stream.place/streamplace/pkg/access"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/log"
	"stream.place/streamplace/pkg/statedb"
)

// RefreshInterval bounds visibility of writes made by another node sharing the
// same authority and database. Request-time expiry checks are immediate.
const RefreshInterval = 30 * time.Second

type installed struct {
	policy      *access.Snapshot
	generations map[string]uint64
	checkedAt   time.Time
}

type Controller struct {
	bootstrap access.Bootstrap
	state     *statedb.StatefulDB
	bus       *bus.Bus
	mu        sync.Mutex
	current   atomic.Pointer[installed]
}

func New(ctx context.Context, bootstrap access.Bootstrap, state *statedb.StatefulDB, b *bus.Bus) (*Controller, error) {
	if _, err := syntax.ParseDID(bootstrap.Authority); err != nil {
		return nil, fmt.Errorf("%w: invalid node authority: %w", access.ErrInvalid, err)
	}
	// Copy configuration through the same ownership boundary as stored policy.
	policy, err := access.NewSnapshot(bootstrap, 0, nil)
	if err != nil {
		return nil, err
	}
	bootstrap.Modes, bootstrap.Entries = policy.Modes(), policy.Entries()
	c := &Controller{bootstrap: bootstrap, state: state, bus: b}
	if err := c.Refresh(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Controller) Snapshot() *access.Snapshot { return c.current.Load().policy }

func (c *Controller) Check(did string, capability access.Capability) access.Decision {
	current := c.current.Load()
	decision := current.policy.Check(did, capability)
	if capability == access.Streamer {
		decision.Generation = current.generations[did]
	}
	return decision
}

func (c *Controller) refresh(ctx context.Context) error {
	revision, entries, generations, err := c.state.ReadAccess(ctx, c.bootstrap.Authority)
	if err != nil {
		return err
	}
	policy, err := access.NewSnapshot(c.bootstrap, revision, entries)
	if err != nil {
		return err
	}
	now := time.Now()
	previous := c.current.Load()
	next := &installed{policy: policy, generations: generations, checkedAt: now}
	if previous != nil && previous.policy.Revision() > revision {
		return fmt.Errorf("access policy revision moved backwards")
	}
	c.current.Store(next)
	if previous == nil || c.bus == nil {
		return nil
	}
	change := access.Change{Revision: revision}
	changed := revision != previous.policy.Revision()
	for _, entry := range append(previous.policy.Entries(), policy.Entries()...) {
		if entry.Capability != access.Chat && entry.Capability != access.Streamer {
			continue
		}
		before := previous.policy.CheckAt(entry.Subject, entry.Capability, previous.checkedAt)
		after := policy.CheckAt(entry.Subject, entry.Capability, now)
		if before.Allowed != after.Allowed {
			changed = true
			if entry.Capability == access.Chat {
				change.ChatChanged = true
			}
		}
	}
	if changed {
		c.bus.Publish(access.Topic, change)
	}

	return nil
}

func (c *Controller) Refresh(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refresh(ctx)
}

func (c *Controller) Create(ctx context.Context, subject string, capability access.Capability, effect access.Effect, reason string, expiresAt *time.Time) (access.Entry, error) {
	entry := access.Entry{Authority: c.bootstrap.Authority, Subject: subject, Capability: capability, Effect: effect, Reason: reason, CreatedAt: time.Now().UTC(), ExpiresAt: expiresAt}
	if err := access.ValidateMutation(entry); err != nil {
		return access.Entry{}, err
	}
	if expiresAt != nil && !expiresAt.After(entry.CreatedAt) {
		return access.Entry{}, fmt.Errorf("%w: expiry must be in the future", access.ErrInvalid)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	saved, err := c.state.CreateAccessEntry(ctx, entry)
	if err != nil {
		return access.Entry{}, err
	}
	if err := c.refresh(ctx); err != nil {
		return access.Entry{}, err
	}
	return saved, nil
}

func (c *Controller) Delete(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.state.DeleteAccessEntry(ctx, c.bootstrap.Authority, id); err != nil {
		return err
	}
	return c.refresh(ctx)
}

func (c *Controller) Run(ctx context.Context) error {
	ticker := time.NewTicker(RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := c.Refresh(ctx); err != nil {
				log.Error(ctx, "refresh node access policy", "err", err)
			}
		}
	}
}
