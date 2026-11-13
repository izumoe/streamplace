package config

import (
	"fmt"
	"strings"

	"stream.place/streamplace/pkg/access"
)

// AccessBootstrap translates startup configuration into independent capabilities.
// VOD retains the configured upload eligibility at startup; later publishing
// grants do not implicitly grant upload or recording rights.
func (cli *CLI) AccessBootstrap() (access.Bootstrap, error) {
	policy := access.Bootstrap{Modes: map[access.Capability]access.Mode{}}
	if cli.ServerHost != "" || cli.BroadcasterHost != "" {
		policy.Authority = cli.ServerDID()
	}
	if len(cli.AllowedStreams) == 0 {
		policy.Modes[access.VOD] = access.Open
	}
	if cli.WideOpen {
		policy.Modes[access.Streamer] = access.Open
		policy.Modes[access.VOD] = access.Open
	}
	add := func(subject string, capability access.Capability) {
		policy.Entries = append(policy.Entries, access.Entry{Subject: subject, Capability: capability, Effect: access.Allow})
	}
	for _, did := range cli.AdminDIDs {
		add(did, access.Admin)
	}
	for _, did := range cli.AllowedStreams {
		add(did, access.Streamer)
		add(did, access.VOD)
	}
	if len(cli.Syndicate) > 0 {
		policy.Modes[access.Syndicate] = access.Allowlist
	}
	for _, did := range cli.Syndicate {
		if did == "*" {
			policy.Modes[access.Syndicate] = access.Open
			continue
		}
		add(did, access.Syndicate)
	}
	seen := map[access.Capability]bool{}
	if cli.AccessPolicy != "" {
		for _, setting := range strings.Split(cli.AccessPolicy, ",") {
			key, value, ok := strings.Cut(strings.TrimSpace(setting), "=")
			cap, mode := access.Capability(key), access.Mode(value)
			if !ok || seen[cap] {
				return policy, fmt.Errorf("%w: duplicate or malformed capability mode %q", access.ErrInvalid, setting)
			}
			if err := access.ValidateMode(cap, mode); err != nil {
				return policy, err
			}
			seen[cap] = true
			policy.Modes[cap] = mode
		}
	}
	if cli.DisableSyndication {
		policy.Modes[access.Syndicate] = access.Off
	}
	_, err := access.NewSnapshot(policy, 0, nil, nil)
	return policy, err
}

// CapabilityAllowed is the startup-only fallback for components without a
// running access controller. Runtime handlers use the controller's snapshot.
func (cli *CLI) CapabilityAllowed(did string, capability access.Capability) bool {
	bootstrap, err := cli.AccessBootstrap()
	if err != nil {
		return false
	}
	policy, err := access.NewSnapshot(bootstrap, 0, nil, nil)
	return err == nil && policy.Check(did, capability).Allowed
}
