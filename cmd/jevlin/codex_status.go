package main

// What `agents status` says about Codex's sandbox.
//
// Three things decide what a command Codex runs can reach: which profile
// default_permissions names, which hosts that profile allows, and whether
// features.network_proxy is on to enforce them. The third is the one that
// fails open: `codex features disable network_proxy` deletes our
// [features.network_proxy] table (seen live), and a profile without it
// lets every sandboxed command reach any host. So status says that in
// those words. The hosts are compared with what the config names now, all
// three of them (router, AS, platform), because a process a sandboxed
// search spawns contacts the AS and the platform too, and on macOS a denied
// CONNECT from it fails whichever tool call is running.

import (
	"fmt"
	"strings"
)

func codexPermissionLines(ops agentOps, path string, entry binEntry, getenv func(string) string) []string {
	lines, damaged := codexPermissionLinesBody(ops, path, entry, getenv)
	return append(damaged, lines...)
}

func codexPermissionLinesBody(ops agentOps, path string, entry binEntry, getenv func(string) string) (lines, damage []string) {
	existing, err := ops.readFile(path)
	if err != nil {
		return nil, damage
	}
	where := tilde(ops.home, path)
	cfg := configForEntry(entry, getenv)
	var want []string
	if cfg != nil {
		want = codexAllowedHosts(cfg)
	}
	region, had, why := readCodexRegion(existing)
	if had && why != "" {
		return []string{"permissions: the jevlin block in " + where + " cannot be read: " + why}, damage
	}
	if !had {
		return nil, damage
	}
	if region.damaged {
		damage = append(damage, "permissions: the jevlin block in "+where+" has lost one of its markers (Codex deletes the comments above a table it removes); agents install repairs it, and agents uninstall removes it whole")
	}
	if region.legacy {
		if legacyNetworkOpen(region) {
			return []string{"permissions: the old sandbox block in " + where + " gives every command Codex runs open network to any host; agents install replaces it with a profile limited to the search hosts"}, damage
		}
		return []string{"permissions: the old sandbox block in " + where + " grants the writable roots and no network; agents install replaces it with a profile"}, damage
	}
	facts, ok := readCodexFacts(region.participantInPlace())
	if !ok {
		return []string{"permissions: " + where + " does not read as TOML outside the jevlin block"}, damage
	}
	active := region.keyPresent || (facts.hasDefault && facts.defaultPermissions == codexProfileName)
	if !active {
		name := "nothing"
		if facts.hasDefault {
			name = mustTOMLString(facts.defaultPermissions)
		}
		return []string{fmt.Sprintf("permissions: default_permissions in %s names %s, not jevlin's profile; Codex's commands do not run under it", where, name)}, damage
	}
	proxyOn := region.proxyPresent || facts.proxyOn()
	state := "on"
	if !proxyOn {
		state = "off"
	}
	switch {
	case region.network:
		allowed := codexAllowedFor(facts.doc, region.extends, region.hosts)
		hosts := orNone(allowed.hosts)
		if allowed.every {
			hosts = allowed.sentence()
		}
		lines = append(lines, fmt.Sprintf("permissions: profile %q (extends %s) in %s; hosts %s; network_proxy %s", codexProfileName, mustTOMLString(region.extends), where, hosts, state))
		if !proxyOn {
			lines = append(lines, "permissions: network_proxy is off in "+where+", so the profile's host list is not enforced and every command Codex runs can reach any host; agents install turns it on")
		}
		if want != nil && !sameStrings(cleanHosts(region.hosts), want) {
			lines = append(lines, fmt.Sprintf("permissions: the profile allows %s, but the config now names %s; agents install refreshes it", orNone(region.hosts), orNone(want)))
		}
	case profileNetworkOn(facts.doc, region.extends) && !proxyOn:
		lines = append(lines, fmt.Sprintf("permissions: profile %q (extends %s) in %s adds writable roots only; the network is open to every host by your profile %s's own setting, which jevlin leaves",
			codexProfileName, mustTOMLString(region.extends), where, mustTOMLString(region.extends)))
	default:
		lines = append(lines, fmt.Sprintf("permissions: profile %q (extends %s) in %s grants the writable roots and no network, so a search from Codex cannot reach the router; agents install asks to add the search hosts",
			codexProfileName, mustTOMLString(region.extends), where))
	}
	return lines, damage
}

func mustDecode(s string) tomlDoc {
	doc, _ := decodeTOMLDoc(s)
	return doc
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
