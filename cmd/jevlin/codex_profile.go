package main

// What this client writes into Codex's config.toml on macOS and Linux: a
// permission profile, not a widened workspace-write sandbox.
//
// The block this replaces, `[sandbox_workspace_write] network_access = true`,
// opened the network to every command Codex ran in its sandbox, in every
// project, to any host (seen live: example.com answered 200 from inside the
// sandbox, DNS resolved, a raw socket connected). The search needs exactly
// the hosts a sandboxed search and the processes it spawns can reach, and
// Codex has a shape for that: `default_permissions` names a profile, the
// profile's `network.domains` is an allowlist, and `features.network_proxy`
// makes Codex route every sandboxed command through a loopback proxy that
// enforces it. With the feature on, the sandbox is a network namespace
// holding only loopback; an off-list host gets a CONNECT 403 and Codex fails
// the tool call. Without the feature the same profile is wide open, which is
// why the feature table is part of what is rendered and why its absence is a
// finding (codexProfileStatus) and a refresh (planCodexProfile), not a
// detail.
//
// The hosts are derived from the config and from nothing else: the
// router the search posts to; the AS, which the flush a search spawns
// reaches for its discovery document, token and submission endpoints, all
// same-origin checked; and the platform's agents API, which the detached
// `connect -resume` a search spawns reaches. Those are the only network
// destinations on the path from a sandboxed search, and the list carries
// hard invariant 1: on macOS a detached child outlives the command that
// spawned it, and a denied CONNECT from it fails whichever tool call is
// running at that moment, the search's own or a later, unrelated one.

import (
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

// codexProfileName is the profile this client writes and names in
// default_permissions. The name is not what makes a profile ours — a second
// installation writes the same name — the directories in its filesystem
// table are (codexRootsOwner).
const codexProfileName = "jevlin"

// codexProfileFloor is the oldest Codex whose config loader accepts every
// key the region carries. `default_permissions` is read from 0.113.0;
// `[features.network_proxy]` as a table from 0.131.0 — 0.130.0 and older
// refuse the whole file with codexProfileFloorError, and Codex does not
// start until the block is removed. No file on disk records the installed
// version (~/.codex/version.json holds only the update check's answer), and
// this client does not run codex to ask, so the floor is documented and the
// error named rather than checked.
const (
	codexProfileFloor      = "0.131.0"
	codexProfileFloorError = "invalid type: map, expected a boolean"
)

// codexProfile is everything the renderer needs: the directories a sandboxed
// search must write, the hosts it and its children may reach, the profile it
// extends, and which of the lines that can also live outside the region are
// written inside it.
type codexProfile struct {
	roots []string
	hosts []string
	// extends is the profile ours builds on: ":workspace", or the
	// participant's own profile that default_permissions named before they
	// said yes to switching. Codex merges the two (seen live on 0.158.0
	// Linux and 0.160.0 macOS): both profiles' writable roots and both
	// domain lists apply, and anything neither lists stays closed.
	extends string
	// key is false when default_permissions is the participant's own line,
	// rewritten to name our profile; the region then carries no key.
	key bool
	// network is false when ours adds no network at all: the profile it
	// extends already has an open one by the participant's own setting
	// (network on, proxy off), which ours must not cut down to our hosts;
	// or a safe form that closes a network this client opened.
	network bool
	// proxy is false when the participant's own [features] table already
	// turns network_proxy on, or was rewritten to; a second definition
	// would be a duplicate key and Codex would refuse the file.
	proxy bool
}

func fullCodexProfile(roots, hosts []string) codexProfile {
	return codexProfile{roots: roots, hosts: hosts, extends: ":workspace", key: true, network: true, proxy: true}
}

// codexAllowedHosts is the list, from the config and nothing else. A
// loopback host is never listed: Codex's proxy would then let every
// sandboxed command reach any port on this machine, and it would not even
// serve the search, because Go never sends a request for a loopback target
// through a proxy (codexLoopbackServices names them for the plan).
func codexAllowedHosts(cfg *config.Config) []string {
	var hosts []string
	for _, s := range codexServices(cfg) {
		if !isLoopbackName(s.host) {
			hosts = append(hosts, s.host)
		}
	}
	return cleanHosts(hosts)
}

// codexLoopbackServices names the services the config puts on this
// machine's loopback, which a sandboxed command cannot reach.
func codexLoopbackServices(cfg *config.Config) []string {
	var out []string
	for _, s := range codexServices(cfg) {
		if isLoopbackName(s.host) {
			out = append(out, s.name+" ("+s.host+")")
		}
	}
	return out
}

type codexService struct{ name, host string }

// codexServices is every destination a sandboxed search and the processes
// it spawns can reach: the router; the authorization server when as_url is
// set (the flush); the platform's agents API (the claim resume).
func codexServices(cfg *config.Config) []codexService {
	var out []codexService
	if cfg.Miner.RouterURL != nil {
		out = append(out, codexService{"the router", cfg.Miner.RouterURL.Hostname()})
	}
	if cfg.Mining.ASBaseURL != "" {
		if u, err := url.Parse(cfg.Mining.ASBaseURL); err == nil {
			out = append(out, codexService{"the authorization server", u.Hostname()})
		}
	}
	if cfg.Platform.AgentsAPIURL != "" {
		if u, err := url.Parse(cfg.Platform.AgentsAPIURL); err == nil {
			out = append(out, codexService{"the platform's agents API", u.Hostname()})
		}
	}
	return out
}

// isLoopbackName: is host this machine, by name or by any loopback or
// unspecified address?
func isLoopbackName(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// cleanHosts is one cleaning rule for every host list: lowercased, no empty
// name, each named once, sorted — the order the renderer writes them in and
// the order status compares them in.
func cleanHosts(hosts []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// codexProfileHeaders are the table headers the renderer writes, in order,
// spelled as splitMarkedBlock reports them. The region is classified by this
// set and by the one bare key; nothing else between the markers is ours.
func codexProfileHeaders() []string {
	return []string{
		"features.network_proxy",
		"permissions." + codexProfileName,
		"permissions." + codexProfileName + ".filesystem",
		"permissions." + codexProfileName + ".network",
		"permissions." + codexProfileName + ".network.domains",
	}
}

// codexProfileRegion renders the whole marked region: markers, the comment
// that says what it does and the floor, and the profile.
func codexProfileRegion(p codexProfile) []byte {
	var b strings.Builder
	b.WriteString(agentsMarkerBegin + "\n")
	if p.network {
		b.WriteString("# Codex permission profile for jevlin's search: write access to its jevlin\n")
		b.WriteString("# home and network access to the search hosts only, through Codex's proxy.\n")
	} else {
		b.WriteString("# Codex permission profile for jevlin's search: write access to its jevlin\n")
		b.WriteString("# home; its network is the profile it extends, unchanged.\n")
	}
	b.WriteString("# Needs Codex " + codexProfileFloor + " or newer: an older Codex refuses this file with\n")
	b.WriteString("# \"" + codexProfileFloorError + "\" and does not start; remove\n")
	b.WriteString("# this block, or upgrade Codex.\n")
	b.WriteString(codexProfileText(p))
	b.WriteString(agentsMarkerEnd + "\n")
	return []byte(b.String())
}

// codexProfileText is the profile without the markers and the leading
// comment: what the region's own lines decode to, and what a participant is
// shown when asked to add it by hand.
func codexProfileText(p codexProfile) string {
	var b strings.Builder
	if p.key {
		b.WriteString("default_permissions = " + mustTOMLString(codexProfileName) + "\n\n")
	}
	if p.proxy {
		b.WriteString("[features.network_proxy]\nenabled = true\n\n")
	}
	extends := p.extends
	if extends == "" {
		extends = ":workspace"
	}
	b.WriteString("[permissions." + codexProfileName + "]\nextends = " + mustTOMLString(extends) + "\n\n")
	b.WriteString("[permissions." + codexProfileName + ".filesystem]\n")
	for _, r := range p.roots {
		b.WriteString(mustTOMLString(r) + " = \"write\"\n")
	}
	if p.network {
		b.WriteString("\n[permissions." + codexProfileName + ".network]\nenabled = true\n\n")
		b.WriteString("[permissions." + codexProfileName + ".network.domains]\n")
		for _, h := range p.hosts {
			b.WriteString(mustTOMLString(h) + " = \"allow\"\n")
		}
	}
	return b.String()
}

// mustTOMLString quotes s as a TOML basic string. The renderer's inputs are
// paths and host names this binary already validated; a value tomlString
// cannot quote is a programming error here, not a participant's file.
func mustTOMLString(s string) string {
	q, err := tomlString(s)
	if err != nil {
		panic(err)
	}
	return q
}

// codexWindowsSettings is what Windows gets instead of a profile: the
// writable roots alone. No one has seen a permission profile work in Codex's
// Windows sandbox, so none is written there — the same rule as a hook cell
// nothing has established — and the network is not opened either: a search
// there needs Codex's approval to reach the router, and the plan says so.
func codexWindowsSettings(roots []string) string {
	quoted := make([]string, len(roots))
	for i, r := range roots {
		quoted[i] = mustTOMLString(r)
	}
	return "[" + codexSandboxTable + "]\nwritable_roots = [" + strings.Join(quoted, ", ") + "]\n"
}
