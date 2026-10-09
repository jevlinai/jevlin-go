package main

// What `agents status` says about Codex's sandbox (D5, C2, C3).
//
// Three things decide what a command Codex runs can reach: which profile
// default_permissions names, which hosts that profile allows, and whether
// features.network_proxy is on to enforce them. The third is the one that
// fails open: `codex features disable network_proxy` deletes our
// [features.network_proxy] table (seen live, C2), and a profile without it
// lets every sandboxed command reach any host. So status says that in
// those words. The hosts are compared with what the config names now, all
// three of them (router, AS, platform), because a process a sandboxed
// search spawns contacts the AS and the platform too, and on macOS a denied
// CONNECT from it fails whichever tool call is running (C3).

import (
	"fmt"
	"strings"
)

func codexPermissionLines(ops agentOps, path string, entry binEntry, getenv func(string) string) []string {
	existing, err := ops.readFile(path)
	if err != nil {
		return nil
	}
	cfg := configForEntry(entry, getenv)
	var want []string
	if cfg != nil {
		want = codexAllowedHosts(cfg)
	}
	region, had, why := readCodexRegion(existing)
	if had && why != "" {
		return []string{"permissions: the jevlin block in config.toml cannot be read: " + why}
	}
	stripped, _ := removeMarkedBlock(existing)
	facts, ok := readCodexFacts(string(stripped))
	if !ok {
		return []string{"permissions: config.toml does not read as TOML"}
	}
	if had && region.legacy {
		return []string{"permissions: the old sandbox block gives every command Codex runs open network to any host; agents install replaces it with a profile limited to the search hosts"}
	}
	var name string
	var hosts []string
	var proxyOn bool
	switch {
	case had:
		name = codexProfileName
		if !region.keyPresent {
			name = facts.defaultPermissions
		}
		hosts = region.hosts
		proxyOn = region.proxyPresent || facts.proxy == proxyBoolTrue || facts.proxy == proxyTableTrue
	case facts.hasDefault && !strings.HasPrefix(facts.defaultPermissions, ":"):
		name = facts.defaultPermissions
		hosts = keysWithValue(facts.doc, "allow", "permissions", name, "network", "domains")
		proxyOn = facts.proxy == proxyBoolTrue || facts.proxy == proxyTableTrue
	default:
		return nil
	}
	var lines []string
	if had && name != codexProfileName {
		lines = append(lines, fmt.Sprintf("permissions: default_permissions names %q, not jevlin's profile; Codex's commands run under that one", name))
	}
	state := "on"
	if !proxyOn {
		state = "off"
	}
	lines = append(lines, fmt.Sprintf("permissions: profile %q; hosts %s; network_proxy %s", name, orNone(hosts), state))
	if !proxyOn {
		where := "the [features.network_proxy] table is gone"
		if facts.proxy == proxyBoolFalse || facts.proxy == proxyTableFalse {
			where = "it is off in your [features] table"
		}
		fix := "agents install turns it on"
		if !had {
			// A profile of the participant's own with its network on and
			// the proxy off is unrestricted by their choice, and install
			// leaves it so (C4 ii).
			fix = "that is your profile's own setting, which jevlin leaves"
		}
		lines = append(lines, "permissions: network_proxy is off ("+where+"), so the profile's host list is not enforced and every command Codex runs can reach any host; "+fix)
	}
	if want == nil {
		return lines
	}
	if had {
		// Ours: exactly the hosts the config names, no more and no fewer.
		if !sameStrings(cleanHosts(hosts), want) {
			lines = append(lines, fmt.Sprintf("permissions: the profile allows %s, but the config now names %s; agents install refreshes it", orNone(hosts), orNone(want)))
		}
		return lines
	}
	// The participant's: their own hosts are theirs; only ours can be missing.
	var missing []string
	for _, h := range want {
		if !containsString(cleanHosts(hosts), h) {
			missing = append(missing, h)
		}
	}
	if len(missing) > 0 && proxyOn {
		lines = append(lines, fmt.Sprintf("permissions: the profile does not allow %s, which the config names; agents install asks to add it", strings.Join(missing, ", ")))
	}
	return lines
}

func orNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
