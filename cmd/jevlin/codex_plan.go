package main

// Planning Codex's config.toml: which of the file's starting states this is,
// what it needs, whom to ask, and what the plan says.
//
// The decisions are the maintainer's (D3–D7, C4–C6), in one order:
//
//   - Windows: no profile, the writable roots alone (D6).
//   - A marked region that cannot be read: left and reported; the skill and
//     hooks are still written, as before.
//   - A region whose roots are another installation's: leaveToItsOwner —
//     nothing for Codex, and the sentence names the owner (C6).
//   - A [sandbox_workspace_write] table of the participant's, or a
//     read-only choice (`sandbox_mode = "read-only"`,
//     `default_permissions = ":read-only"`): nothing for Codex, with the
//     profile printed to add by hand; widening read-only is theirs to do.
//   - Full access (`danger-full-access`, either spelling): the skill and the
//     hooks only; there is nothing to widen.
//   - `default_permissions = ":workspace"`, a bare
//     `sandbox_mode = "workspace-write"`, `default_permissions` naming a
//     profile of the participant's, or `[features] network_proxy = false`:
//     a setting of theirs the profile would have to change, so nothing for
//     Codex, with the lines to use printed.
//   - Otherwise: the region, fresh, refreshed, or migrated from the old
//     block (D7).

import (
	"fmt"
	"runtime"
	"strings"
)

// codexSandboxOS is the OS whose Codex sandbox install and status plan for:
// a profile everywhere but Windows (D6). A variable so a test on any runner
// can plan either answer through the real commands; production never
// changes it.
var codexSandboxOS = runtime.GOOS

// codexScope is how much of Codex an install goes on to write.
type codexScope int

const (
	codexAll            codexScope = iota // config, skill and hooks
	codexSkillHooksOnly                   // the sandbox needs nothing
	codexNothing                          // install nothing for Codex
)

// codexConfigPlan is what planning the config concluded.
type codexConfigPlan struct {
	scope   codexScope
	changed bool // a write to config.toml was planned
	left    bool // something in config.toml was left as it is, and said so
}

// codexWriteWhy is the plan line for the fresh region.
func codexWriteWhy(hosts []string) string {
	return fmt.Sprintf("permissions: profile %q with the writable roots, and network to %s only, through Codex's proxy (needs Codex %s or newer)",
		codexProfileName, joinLabels(hosts), codexProfileFloor)
}

// planCodexConfig plans Codex's config.toml for goos and says how much of
// the rest of Codex may follow.
func planCodexConfig(ops agentOps, label, path string, entry binEntry, getenv func(string) string, goos string, p *agentPlan) codexConfigPlan {
	roots := codexSandboxRoots(entry, getenv)
	if len(roots) == 0 {
		if why := codexSandboxProblem(entry, getenv); why != "" {
			p.notes = append(p.notes, label+": not widening the sandbox: "+why)
		} else {
			p.notes = append(p.notes, label+": shell commands run sandboxed; if searches record nothing, allow this command network access and let it write to your jevlin home")
		}
		return codexConfigPlan{}
	}
	if goos == "windows" {
		changed, left := planCodexWindowsRoots(ops, label, path, roots, entry, getenv, p)
		return codexConfigPlan{changed: changed, left: left}
	}
	cfg := configForEntry(entry, getenv)
	hosts := codexAllowedHosts(cfg)
	existing, mode, err := readWithMode(ops, path)
	if err != nil {
		p.refused = append(p.refused, fmt.Sprintf("%s: cannot read %s: %v", label, path, err))
		return codexConfigPlan{left: true}
	}
	region, had, why := readCodexRegion(existing)
	if had && why != "" {
		p.refused = append(p.refused, fmt.Sprintf("%s: the jevlin block in %s is left as it is: %s", label, path, why))
		return codexConfigPlan{left: true}
	}
	if had {
		// A region that already reads as this binary would write it needs
		// no owner; everything else is attributed before it is touched.
		if ours, other, why := codexRootsOwner(region.roots, entry, getenv); !ours {
			if other == "" {
				p.notes = append(p.notes, fmt.Sprintf("%s: left the jevlin block in %s, and installed nothing else for Codex: %s", label, path, why))
			} else {
				noteOnce(p, label+": "+leftForeign(other)+"; nothing else was installed for Codex, whose searches go through that installation")
			}
			return codexConfigPlan{scope: codexNothing, left: true}
		}
	}
	stripped, _ := removeMarkedBlock(existing)
	facts, ok := readCodexFacts(string(stripped))
	if !ok {
		p.refused = append(p.refused, fmt.Sprintf("%s: %s does not read as TOML outside the jevlin block, so nothing was written to it", label, path))
		return codexConfigPlan{left: true}
	}
	profile := fullCodexProfile(roots, hosts)
	byHand := indentBlock(codexProfileText(profile))
	// The region carries default_permissions only when no line outside it
	// sets one: after ":workspace" was rewritten to name our profile, that
	// marked line is the key, and a second one would be a duplicate Codex
	// refuses.
	profile.key = !facts.hasDefault

	// What the participant's own file already decides.
	switch {
	case facts.wwTable:
		p.refused = append(p.refused, fmt.Sprintf("%s: %s has a [%s] table of your own, which Codex does not combine with a permission profile; nothing was installed for Codex. To use jevlin's search there, replace that table with this profile:\n%s",
			label, path, codexSandboxTable, byHand))
		return codexConfigPlan{scope: codexNothing, left: true}
	case facts.hasSandboxMode && facts.sandboxMode == "danger-full-access",
		facts.hasDefault && facts.defaultPermissions == ":danger-full-access":
		p.notes = append(p.notes, label+": your Codex sandbox is danger-full-access, so there is nothing to widen; the skill and hooks are enough")
		return codexConfigPlan{scope: codexSkillHooksOnly}
	case facts.hasSandboxMode && facts.sandboxMode == "read-only",
		facts.hasDefault && facts.defaultPermissions == ":read-only":
		p.refused = append(p.refused, fmt.Sprintf("%s: %s keeps Codex's sandbox read-only, and widening that is yours to decide; nothing was installed for Codex. To use jevlin's search there, replace it with this profile:\n%s", label, path, byHand))
		return codexConfigPlan{scope: codexNothing, left: true}
	case facts.hasSandboxMode && facts.sandboxMode != "workspace-write":
		p.refused = append(p.refused, fmt.Sprintf("%s: %s sets sandbox_mode = %q, which this client does not know how to combine with a permission profile; nothing was installed for Codex", label, path, facts.sandboxMode))
		return codexConfigPlan{scope: codexNothing, left: true}
	case facts.hasDefault && strings.HasPrefix(facts.defaultPermissions, ":") && facts.defaultPermissions != ":workspace":
		p.refused = append(p.refused, fmt.Sprintf("%s: %s names the built-in profile %q in default_permissions, which this client does not know how to widen; nothing was installed for Codex", label, path, facts.defaultPermissions))
		return codexConfigPlan{scope: codexNothing, left: true}
	}

	// A setting of the participant's own that the profile would have to
	// change is theirs to change: nothing is installed for Codex, and the
	// lines to use are printed.
	switch {
	case facts.hasDefault && facts.defaultPermissions == ":workspace":
		p.refused = append(p.refused, fmt.Sprintf("%s: %s sets default_permissions = \":workspace\"; nothing was installed for Codex. To use jevlin's search there, set default_permissions = %q and add this profile:\n%s", label, path, codexProfileName, byHand))
		return codexConfigPlan{scope: codexNothing, left: true}
	case facts.hasDefault:
		p.refused = append(p.refused, fmt.Sprintf("%s: %s names a permission profile of yours, %q, in default_permissions; nothing was installed for Codex. To use jevlin's search there, give that profile the writable roots and network hosts of this one:\n%s", label, path, facts.defaultPermissions, byHand))
		return codexConfigPlan{scope: codexNothing, left: true}
	case facts.hasSandboxMode: // "workspace-write", the only value left
		p.refused = append(p.refused, fmt.Sprintf("%s: %s sets sandbox_mode = \"workspace-write\", which Codex does not combine with a permission profile; nothing was installed for Codex. To use jevlin's search there, remove sandbox_mode and add this profile:\n%s", label, path, byHand))
		return codexConfigPlan{scope: codexNothing, left: true}
	}
	switch facts.proxy {
	case proxyBoolTrue, proxyTableTrue:
		profile.proxy = false
	case proxyBoolFalse, proxyTableFalse:
		p.refused = append(p.refused, fmt.Sprintf("%s: your [features] table in %s turns network_proxy off, and without it a profile opens the sandbox network to every host; nothing was installed for Codex. To use jevlin's search there, set network_proxy = true there and add this profile:\n%s", label, path, indentBlock(codexProfileText(codexProfile{roots: roots, hosts: hosts, key: true}))))
		return codexConfigPlan{scope: codexNothing, left: true}
	case proxyOther:
		p.refused = append(p.refused, fmt.Sprintf("%s: features.network_proxy in %s has a shape this client does not read; nothing was installed for Codex", label, path))
		return codexConfigPlan{scope: codexNothing, left: true}
	}

	next, change, why := installCodexRegion(existing, codexProfileRegion(profile))
	if why != "" {
		p.refused = append(p.refused, fmt.Sprintf("%s: %s: %s", label, path, why))
		return codexConfigPlan{left: true}
	}
	if change.unchanged {
		return codexConfigPlan{}
	}
	why = codexWriteWhy(hosts)
	switch {
	case change.migrated:
		why = fmt.Sprintf("permissions: replace the sandbox block, which gave every Codex command open network, with profile %q: only %s, through Codex's proxy (needs Codex %s or newer)",
			codexProfileName, joinLabels(hosts), codexProfileFloor)
	case change.proxyRestored:
		why = fmt.Sprintf("permissions: restore [features.network_proxy], which had been turned off: without it every command Codex runs could reach any host; the profile's hosts, %s, are enforced again", joinLabels(hosts))
	}
	if len(change.droppedKeys) > 0 {
		p.notes = append(p.notes, droppedKeysNote(label, path, change.droppedKeys))
	}
	if len(change.movedRoots) > 0 {
		p.notes = append(p.notes, fmt.Sprintf("%s: moving %s Codex wrote inside the jevlin block in %s to just above it: %s", label, keysWord(len(change.movedRoots)), path, strings.Join(change.movedRoots, ", ")))
	}
	if len(change.movedTables) > 0 {
		p.notes = append(p.notes, fmt.Sprintf("%s: moving %s out of the jevlin block in %s, below it, so a later append by Codex lands outside ours: %s",
			label, tables(len(change.movedTables)), path, strings.Join(change.movedTables, ", ")))
	}
	return codexConfigPlan{changed: planWrite(ops, label, path, next, mode, why, p)}
}

func keysWord(n int) string {
	if n == 1 {
		return "1 key"
	}
	return fmt.Sprintf("%d keys", n)
}

// planCodexWindowsRoots is D6: the old table, without the network key,
// where no profile has been seen to work, written where it is found as the
// old block always was. The note says what the missing network costs.
func planCodexWindowsRoots(ops agentOps, label, path string, roots []string, entry binEntry, getenv func(string) string, p *agentPlan) (changed, left bool) {
	changed, left = planCodexSandbox(ops, label, path, roots, entry, getenv, p)
	if changed {
		p.notes = append(p.notes, label+": "+codexWindowsNetworkSentence)
	}
	return changed, left
}

const codexWindowsNetworkSentence = "on Windows no permission profile has been seen to work in Codex's sandbox, so none is written and the network is not opened: a search there needs Codex's approval to reach the router"

// codexProxyState is what the file outside our region says about
// features.network_proxy.
type codexProxyState int

const (
	proxyUnset codexProxyState = iota
	proxyBoolTrue
	proxyBoolFalse
	proxyTableTrue
	proxyTableFalse
	proxyOther // a shape this client does not read
)

// codexFacts is what the file says outside our region, decoded.
type codexFacts struct {
	doc                tomlDoc
	defaultPermissions string
	hasDefault         bool
	sandboxMode        string
	hasSandboxMode     bool
	wwTable            bool // a [sandbox_workspace_write] table that is not inside our markers
	proxy              codexProxyState
}

func readCodexFacts(stripped string) (codexFacts, bool) {
	doc, ok := decodeTOMLDoc(stripped)
	if !ok {
		return codexFacts{}, false
	}
	f := codexFacts{doc: doc}
	if v, present := doc["default_permissions"]; present {
		f.hasDefault = true
		f.defaultPermissions, _ = v.(string)
	}
	if v, present := doc["sandbox_mode"]; present {
		f.hasSandboxMode = true
		f.sandboxMode, _ = v.(string)
	}
	_, f.wwTable = doc[codexSandboxTable]
	switch v, present := lookupTOMLPath(doc, "features", "network_proxy"); {
	case !present:
		f.proxy = proxyUnset
	default:
		switch x := v.(type) {
		case bool:
			f.proxy = proxyBoolFalse
			if x {
				f.proxy = proxyBoolTrue
			}
		case map[string]any:
			enabled, isBool := x["enabled"].(bool)
			switch {
			case !isBool:
				f.proxy = proxyOther
			case enabled:
				f.proxy = proxyTableTrue
			default:
				f.proxy = proxyTableFalse
			}
		default:
			f.proxy = proxyOther
		}
	}
	return f, true
}
