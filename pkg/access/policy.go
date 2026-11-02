// Package access defines independent node capabilities and their effective policy.
package access

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/bluesky-social/indigo/atproto/syntax"
)

type Capability string

const (
	Admin     Capability = "admin"
	Viewer    Capability = "viewer"
	Chat      Capability = "chat"
	Streamer  Capability = "streamer"
	Syndicate Capability = "syndicate"
	VOD       Capability = "vod"
)

var Capabilities = []Capability{Admin, Viewer, Chat, Streamer, Syndicate, VOD}

type Mode string

const (
	Open      Mode = "open"
	Allowlist Mode = "allowlist"
	Off       Mode = "off"
)

type Effect string

const (
	Allow Effect = "allow"
	Deny  Effect = "deny"

	Configuration = "configuration"
	Stored        = "stored"
	Topic         = "node-access"
)

var (
	ErrInvalid  = errors.New("invalid access policy")
	ErrConflict = errors.New("access entry changed")
	ErrNotFound = errors.New("access entry not found")
)

// Entry is an allowance or restriction owned by this node, not a repo record.
type Entry struct {
	ID         string
	Authority  string
	Subject    string
	Capability Capability
	Effect     Effect
	Reason     string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	Source     string
}

type Bootstrap struct {
	Authority string
	Modes     map[Capability]Mode
	Entries   []Entry
}

type Decision struct {
	Allowed bool
	Reason  string
	// Generation invalidates an admitted publisher session even after reapproval.
	Generation uint64
}

// Checker answers from current memory state without database or network reads.
type Checker interface {
	Check(did string, capability Capability) Decision
}

// Change wakes local ingest and chat delivery after the effective policy changes.
type Change struct {
	Revision    uint64
	ChatChanged bool
}

type entryKey struct {
	subject string
	cap     Capability
}

// Snapshot owns its inputs; callers cannot mutate the policy used for decisions.
type Snapshot struct {
	revision uint64
	modes    map[Capability]Mode
	entries  []Entry
	byKey    map[entryKey][]Entry
}

func NewSnapshot(config Bootstrap, revision uint64, entries []Entry) (*Snapshot, error) {
	if _, err := syntax.ParseDID(config.Authority); config.Authority != "" && err != nil {
		return nil, fmt.Errorf("%w: invalid node authority: %w", ErrInvalid, err)
	}
	s := &Snapshot{
		revision: revision,
		modes: map[Capability]Mode{Admin: Allowlist, Viewer: Open, Chat: Open,
			Streamer: Allowlist, Syndicate: Off, VOD: Allowlist},
		byKey: map[entryKey][]Entry{},
	}
	for capability, mode := range config.Modes {
		if err := ValidateMode(capability, mode); err != nil {
			return nil, err
		}
		s.modes[capability] = mode
	}
	for _, source := range []struct {
		entries []Entry
		name    string
	}{{config.Entries, Configuration}, {entries, Stored}} {
		for _, entry := range source.entries {
			if err := validateEntry(entry); err != nil {
				return nil, err
			}
			if source.name == Stored {
				if err := ValidateMutation(entry); err != nil {
					return nil, err
				}
			}
			if entry.Authority != "" && entry.Authority != config.Authority {
				return nil, fmt.Errorf("%w: entry belongs to another node", ErrInvalid)
			}
			entry.Authority = config.Authority
			entry.Source = source.name
			entry = cloneEntry(entry)
			s.entries = append(s.entries, entry)
			key := entryKey{entry.Subject, entry.Capability}
			s.byKey[key] = append(s.byKey[key], entry)
		}
	}
	return s, nil
}

func ValidateMode(capability Capability, mode Mode) error {
	if !slices.Contains(Capabilities, capability) || (mode != Open && mode != Allowlist && mode != Off) {
		return fmt.Errorf("%w: unknown capability or mode %q=%q", ErrInvalid, capability, mode)
	}
	if capability == Admin && mode != Allowlist {
		return fmt.Errorf("%w: administrators require an allowlist", ErrInvalid)
	}
	if capability == Viewer && mode != Open {
		return fmt.Errorf("%w: restricted viewing is not implemented", ErrInvalid)
	}
	return nil
}

func validateEntry(entry Entry) error {
	if _, err := syntax.ParseDID(entry.Subject); err != nil {
		return fmt.Errorf("%w: invalid subject: %w", ErrInvalid, err)
	}
	if !slices.Contains(Capabilities, entry.Capability) || (entry.Effect != Allow && entry.Effect != Deny) {
		return fmt.Errorf("%w: unknown capability or effect", ErrInvalid)
	}
	if entry.Capability == Viewer || (entry.Capability == Admin && entry.Effect == Deny) {
		return fmt.Errorf("%w: unsupported restriction", ErrInvalid)
	}
	if len(entry.Reason) > 1024 {
		return fmt.Errorf("%w: reason exceeds 1024 bytes", ErrInvalid)
	}
	return nil
}

// ValidateMutation limits administrative writes to fully enforced capabilities.
func ValidateMutation(entry Entry) error {
	if err := validateEntry(entry); err != nil {
		return err
	}
	if entry.Capability != Streamer && entry.Capability != Chat {
		return fmt.Errorf("%w: %s entries are configured at startup", ErrInvalid, entry.Capability)
	}
	return nil
}

func (s *Snapshot) Check(did string, capability Capability) Decision {
	return s.CheckAt(did, capability, time.Now())
}

func (s *Snapshot) CheckAt(did string, capability Capability, now time.Time) Decision {
	mode, known := s.modes[capability]
	if !known {
		return Decision{Reason: "unknown capability"}
	}
	if mode == Off {
		return Decision{Reason: "capability disabled"}
	}
	if capability == Viewer && mode == Open {
		return Decision{Allowed: true, Reason: "public viewing"}
	}
	if did == "" {
		return Decision{Reason: "identity required"}
	}
	if _, err := syntax.ParseDID(did); err != nil {
		return Decision{Reason: "invalid identity"}
	}
	allowed := false
	for _, entry := range s.byKey[entryKey{did, capability}] {
		if entry.ExpiresAt != nil && !now.Before(*entry.ExpiresAt) {
			continue
		}
		if entry.Effect == Deny {
			return Decision{Reason: "explicit node restriction"}
		}
		allowed = true
	}
	if allowed || (mode == Open && !strings.HasPrefix(did, "did:key:")) {
		return Decision{Allowed: true, Reason: "permitted"}
	}
	return Decision{Reason: "account not approved"}
}

func (s *Snapshot) Revision() uint64 { return s.revision }

func (s *Snapshot) Modes() map[Capability]Mode { return maps.Clone(s.modes) }

func cloneEntry(entry Entry) Entry {
	if entry.ExpiresAt != nil {
		expiry := *entry.ExpiresAt
		entry.ExpiresAt = &expiry
	}
	return entry
}

func (s *Snapshot) Entries() []Entry {
	entries := make([]Entry, len(s.entries))
	for i, entry := range s.entries {
		entries[i] = cloneEntry(entry)
	}
	return entries
}
