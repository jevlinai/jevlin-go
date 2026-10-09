package main

// Planning Codex's config.toml: which of the file's starting states this is,
// what it needs, whom to ask, and what the plan says.
//
// The decisions are the maintainer's, in one order:
//
//   - Windows: no profile, the writable roots alone.
//   - A marked region that cannot be read: left and reported; the skill and
//     hooks are still written, as before.
//   - A region whose roots are another installation's: leaveToItsOwner —
//     nothing for Codex, and the sentence names the owner.
//   - A [sandbox_workspace_write] table of the participant's, or a
//     read-only choice (`sandbox_mode = "read-only"`,
//     `default_permissions = ":read-only"`): nothing for Codex, with the
//     profile printed to add by hand; widening read-only is theirs to do.
//   - Full access (`danger-full-access`, either spelling): the skill and the
//     hooks only; there is nothing to widen.
//   - `default_permissions = ":workspace"`, or a bare
//     `sandbox_mode = "workspace-write"`: asked; on a typed yes the one line
//     is rewritten (or commented out), marked, and the profile is written.
//   - `default_permissions` naming a profile of the participant's: asked;
//     on a typed yes the missing entries are added to it, marked.
//   - `[features] network_proxy = false`: asked; on yes rewritten to true,
//     marked, and our region carries no features table.
//   - Otherwise: the region, fresh, refreshed, or migrated from the old
//     block.
//
// A typed no installs nothing for Codex and exits 0; an unanswered question
// stops the whole command, exit 2, with nothing written (hard invariant 18);
// -yes answers none of these questions; with no terminal the Codex part is
// refused and the lines to add by hand are printed.

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// codexSandboxOS is the OS whose Codex sandbox install and status plan for:
// a profile everywhere but Windows. A variable so a test on any runner
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

// consentAnswer is what asking the participant came to.
type consentAnswer int

const (
	consentYes consentAnswer = iota
	consentNo
	consentAborted // no line was typed: the command stops
	consentUnasked // no terminal to ask at
)

// askConsent puts question to the participant through ops.consent. The
// question ends with "[y/N]: "; only a typed y or yes is a yes.
func askConsent(ops agentOps, question string) consentAnswer {
	if ops.consent == nil {
		return consentUnasked
	}
	line, err := ops.consent(question)
	if err != nil {
		return consentAborted
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return consentYes
	}
	return consentNo
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

	ed, err := newCodexEditor(string(existing), entry.cfg)
	if err != nil {
		p.refused = append(p.refused, fmt.Sprintf("%s: %s: %v; nothing was written to it", label, path, err))
		return codexConfigPlan{left: true}
	}
	var edits []string // what the participant's own lines get, for the plan

	// A profile of the participant's own: entries into it, never a region.
	// A profile named jevlin that no marked region of ours holds is the
	// participant's too.
	if facts.hasDefault && facts.defaultPermissions != ":workspace" && !had {
		return planCodexParticipantProfile(ops, label, path, entry, facts, roots, hosts, ed, mode, p)
	}

	if facts.hasDefault && facts.defaultPermissions == ":workspace" {
		q := fmt.Sprintf("\n%s: %s sets default_permissions = \":workspace\", Codex's built-in workspace profile.\njevlin's search needs a profile that extends it with write access to its jevlin home and network\naccess to %s only, through Codex's proxy:\n%s\nReplace \":workspace\" with jevlin's profile %q? The line is marked, and agents uninstall puts \":workspace\" back. [y/N]: ",
			label, tilde(ops.home, path), joinLabels(hosts), byHand, codexProfileName)
		switch askConsent(ops, q) {
		case consentYes:
			if err := ed.rewriteRootKey("default_permissions", mustTOMLString(codexProfileName), codexProfileName); err != nil {
				return codexRefuse(label, path, err, p)
			}
			profile.key = false
			edits = append(edits, "default_permissions rewritten from \":workspace\"")
		case consentNo:
			p.notes = append(p.notes, label+": nothing installed for Codex: you kept default_permissions = \":workspace\"; the skill and hooks were not written")
			return codexConfigPlan{scope: codexNothing}
		case consentAborted:
			p.aborted = promptAbortedReason + "; nothing was changed"
			return codexConfigPlan{scope: codexNothing}
		case consentUnasked:
			p.refused = append(p.refused, fmt.Sprintf("%s: %s sets default_permissions = \":workspace\", and replacing it needs your yes at a terminal (-yes does not answer it); nothing was installed for Codex. By hand: set default_permissions = %q and add this profile:\n%s", label, path, codexProfileName, byHand))
			return codexConfigPlan{scope: codexNothing, left: true}
		}
	}

	if facts.hasSandboxMode { // "workspace-write", the only value left
		q := fmt.Sprintf("\n%s: %s sets sandbox_mode = \"workspace-write\", which Codex does not combine with a permission profile.\njevlin's search needs this profile, which keeps workspace writes and adds write access to its jevlin home\nand network access to %s only, through Codex's proxy:\n%s\nComment out sandbox_mode and use jevlin's profile? The line is marked, and agents uninstall restores it. [y/N]: ",
			label, tilde(ops.home, path), joinLabels(hosts), byHand)
		switch askConsent(ops, q) {
		case consentYes:
			if err := ed.commentOutRootKey("sandbox_mode"); err != nil {
				return codexRefuse(label, path, err, p)
			}
			edits = append(edits, "sandbox_mode commented out")
		case consentNo:
			p.notes = append(p.notes, label+": nothing installed for Codex: you kept sandbox_mode = \"workspace-write\"; the skill and hooks were not written")
			return codexConfigPlan{scope: codexNothing}
		case consentAborted:
			p.aborted = promptAbortedReason + "; nothing was changed"
			return codexConfigPlan{scope: codexNothing}
		case consentUnasked:
			p.refused = append(p.refused, fmt.Sprintf("%s: %s sets sandbox_mode = \"workspace-write\", and replacing it needs your yes at a terminal (-yes does not answer it); nothing was installed for Codex. By hand: remove sandbox_mode and add this profile:\n%s", label, path, byHand))
			return codexConfigPlan{scope: codexNothing, left: true}
		}
	}

	switch facts.proxy {
	case proxyBoolTrue, proxyTableTrue:
		profile.proxy = false
	case proxyBoolFalse, proxyTableFalse:
		q := fmt.Sprintf("\n%s: your [features] table in %s turns network_proxy off, so a permission profile's host list is not enforced\nand Codex's sandbox network is open to every host. jevlin's profile needs it on.\nSet network_proxy = true? The line is marked, and agents uninstall puts false back. [y/N]: ", label, tilde(ops.home, path))
		switch askConsent(ops, q) {
		case consentYes:
			var err error
			if facts.proxy == proxyBoolFalse {
				err = ed.rewriteInSection([]string{"features"}, "network_proxy", "true", true)
			} else {
				err = ed.rewriteInSection([]string{"features", "network_proxy"}, "enabled", "true", true)
			}
			if err != nil {
				return codexRefuse(label, path, err, p)
			}
			profile.proxy = false
			edits = append(edits, "network_proxy rewritten from false")
		case consentNo:
			p.notes = append(p.notes, label+": nothing installed for Codex: you kept network_proxy off, and without it the profile would open the sandbox network to every host; the skill and hooks were not written")
			return codexConfigPlan{scope: codexNothing}
		case consentAborted:
			p.aborted = promptAbortedReason + "; nothing was changed"
			return codexConfigPlan{scope: codexNothing}
		case consentUnasked:
			p.refused = append(p.refused, fmt.Sprintf("%s: your [features] table in %s turns network_proxy off, and turning it on needs your yes at a terminal (-yes does not answer it); nothing was installed for Codex. By hand: set network_proxy = true there and add this profile:\n%s", label, path, indentBlock(codexProfileText(codexProfile{roots: roots, hosts: hosts, key: true}))))
			return codexConfigPlan{scope: codexNothing, left: true}
		}
	case proxyOther:
		p.refused = append(p.refused, fmt.Sprintf("%s: features.network_proxy in %s has a shape this client does not read; nothing was installed for Codex", label, path))
		return codexConfigPlan{scope: codexNothing, left: true}
	}
	if len(edits) > 0 {
		if err := ed.verify(); err != nil {
			return codexRefuse(label, path, err, p)
		}
	}

	next, change, why := installCodexRegion([]byte(ed.text), codexProfileRegion(profile))
	if why != "" {
		p.refused = append(p.refused, fmt.Sprintf("%s: %s: %s", label, path, why))
		return codexConfigPlan{left: true}
	}
	if change.unchanged && len(edits) == 0 {
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
	if len(edits) > 0 {
		why += "; " + strings.Join(edits, ", ")
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

func codexRefuse(label, path string, err error, p *agentPlan) codexConfigPlan {
	p.refused = append(p.refused, fmt.Sprintf("%s: %s: %v", label, path, err))
	return codexConfigPlan{scope: codexNothing, left: true}
}

// planCodexParticipantProfile: default_permissions names a profile of
// the participant's own. The entries it lacks are added to it, marked, after
// a typed yes; nothing else of ours goes into the file.
func planCodexParticipantProfile(ops agentOps, label, path string, entry binEntry, facts codexFacts, roots, hosts []string, ed *codexEditor, mode os.FileMode, p *agentPlan) codexConfigPlan {
	e := missingProfileEntries(facts, roots, hosts)
	if e.empty() {
		return codexConfigPlan{}
	}
	if e.proxy && (facts.proxy == proxyBoolFalse) {
		// Cannot happen: a false bool is case proxyFx. Kept as a guard.
		return codexRefuse(label, path, errors.New("network_proxy is both false and absent"), p)
	}
	q := fmt.Sprintf("\n%s: %s names a permission profile of yours, %q, in default_permissions.\njevlin's search needs these entries in it (each line is marked as jevlin's, and agents uninstall removes only them):\n%s\nAdd these entries to your profile %q? [y/N]: ",
		label, tilde(ops.home, path), e.name, indentBlock(e.text()), e.name)
	switch askConsent(ops, q) {
	case consentNo:
		p.notes = append(p.notes, fmt.Sprintf("%s: nothing installed for Codex: you declined adding jevlin's entries to profile %q; the skill and hooks were not written", label, e.name))
		return codexConfigPlan{scope: codexNothing}
	case consentAborted:
		p.aborted = promptAbortedReason + "; nothing was changed"
		return codexConfigPlan{scope: codexNothing}
	case consentUnasked:
		p.refused = append(p.refused, fmt.Sprintf("%s: %s names a permission profile of yours, %q, and adding jevlin's entries to it needs your yes at a terminal (-yes does not answer it); nothing was installed for Codex. By hand, add:\n%s", label, path, e.name, indentBlock(e.text())))
		return codexConfigPlan{scope: codexNothing, left: true}
	}
	var err error
	apply := func(f func() error) {
		if err == nil {
			err = f()
		}
	}
	if len(e.roots) > 0 {
		var entries []tomlEntry
		for _, r := range e.roots {
			entries = append(entries, tomlEntry{keyTOML: mustTOMLString(r), valueTOML: `"write"`, key: r, value: "write"})
		}
		apply(func() error { return ed.addToSection([]string{"permissions", e.name, "filesystem"}, entries) })
	}
	if e.enable {
		if e.rewrite {
			apply(func() error {
				return ed.rewriteInSection([]string{"permissions", e.name, "network"}, "enabled", "true", true)
			})
		} else {
			apply(func() error {
				return ed.addToSection([]string{"permissions", e.name, "network"}, []tomlEntry{{keyTOML: "enabled", valueTOML: "true", key: "enabled", value: true}})
			})
		}
	}
	if len(e.hosts) > 0 {
		var entries []tomlEntry
		for _, h := range e.hosts {
			entries = append(entries, tomlEntry{keyTOML: mustTOMLString(h), valueTOML: `"allow"`, key: h, value: "allow"})
		}
		apply(func() error { return ed.addToSection([]string{"permissions", e.name, "network", "domains"}, entries) })
	}
	if e.proxy {
		apply(func() error {
			return ed.addToSection([]string{"features", "network_proxy"}, []tomlEntry{{keyTOML: "enabled", valueTOML: "true", key: "enabled", value: true}})
		})
	}
	if e.proxyFx {
		apply(func() error { return ed.rewriteInSection([]string{"features"}, "network_proxy", "true", true) })
	}
	apply(ed.verify)
	if err != nil {
		return codexRefuse(label, path, err, p)
	}
	n := len(e.roots) + len(e.hosts)
	if e.enable {
		n++
	}
	if e.proxy || e.proxyFx {
		n++
	}
	return codexConfigPlan{changed: planWrite(ops, label, path, []byte(ed.text), mode, fmt.Sprintf("permissions: %s added to your profile %q, each marked as jevlin's", entriesWord(n), e.name), p)}
}

func entriesWord(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}

// planCodexWindowsRoots is Windows: the old table, without the network key,
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
