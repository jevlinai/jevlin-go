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
	"fmt"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/jevlinai/jevlin-go/pkg/config"
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

// codexWriteWhy is the plan line for the region.
func codexWriteWhy(profile codexProfile, doc tomlDoc) string {
	base := fmt.Sprintf("permissions: profile %q, extending %s, with the writable roots", codexProfileName, mustTOMLString(profile.extends))
	if profile.network {
		base += fmt.Sprintf(", and network to %s, through Codex's proxy", codexAllowedFor(doc, profile.extends, profile.hosts).sentence())
	} else {
		base += "; its network is your profile's own, which jevlin leaves open"
	}
	return base + fmt.Sprintf(" (supported with Codex %s or newer; older versions are not supported)", codexSupported)
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
	if lo := codexLoopbackServices(cfg); len(lo) > 0 {
		p.notes = append(p.notes, label+": "+joinLabels(lo)+" is on this machine's loopback, which no command in Codex's sandbox can reach; it is not listed in the profile, and a search from Codex cannot use it")
	}
	existing, mode, err := readCodexConfig(ops, path)
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
		if n := staleRootsNote(label, path, region.roots, entry, getenv); n != "" {
			p.notes = append(p.notes, n)
		}
	}
	// The participant's own settings, read with every root key and table
	// they or Codex wrote — including what Codex wrote inside our markers,
	// which install is about to move out, so a sandbox_mode found there is
	// asked about now rather than on the next run.
	participant := string(existing)
	if had {
		participant = region.participantInPlace()
	}
	facts, ok := readCodexFacts(participant)
	if !ok {
		p.refused = append(p.refused, fmt.Sprintf("%s: %s does not read as TOML outside the jevlin block, so nothing was written to it", label, path))
		return codexConfigPlan{left: true}
	}
	profile := fullCodexProfile(roots, hosts)

	// What the participant's own file already decides, and leaves nothing
	// to ask.
	switch {
	case facts.wwTable:
		p.refused = append(p.refused, fmt.Sprintf("%s: %s has a [%s] table of your own, which Codex does not combine with a permission profile; nothing was installed for Codex. To use jevlin's search there, replace that table with this profile, before your first table:\n%s",
			label, path, codexSandboxTable, indentBlock(codexProfileText(profile))))
		planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)

		return codexConfigPlan{scope: codexNothing, left: true}
	case facts.hasSandboxMode && facts.sandboxMode == "danger-full-access",
		facts.hasDefault && facts.defaultPermissions == ":danger-full-access":
		p.notes = append(p.notes, label+": your Codex sandbox is danger-full-access, so there is nothing to widen; the skill and hooks are enough")
		planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)

		return codexConfigPlan{scope: codexSkillHooksOnly}
	case facts.hasSandboxMode && facts.sandboxMode == "read-only",
		facts.hasDefault && facts.defaultPermissions == ":read-only":
		p.refused = append(p.refused, fmt.Sprintf("%s: %s keeps Codex's sandbox read-only, and widening that is yours to decide; nothing was installed for Codex. To use jevlin's search there, replace it with this profile, before your first table:\n%s", label, path, indentBlock(codexProfileText(profile))))
		planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)

		return codexConfigPlan{scope: codexNothing, left: true}
	case facts.hasSandboxMode && facts.sandboxMode != "workspace-write":
		p.refused = append(p.refused, fmt.Sprintf("%s: %s sets sandbox_mode = %q, which this client does not know how to combine with a permission profile; nothing was installed for Codex", label, path, facts.sandboxMode))
		planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)

		return codexConfigPlan{scope: codexNothing, left: true}
	case facts.hasDefault && strings.HasPrefix(facts.defaultPermissions, ":") && facts.defaultPermissions != ":workspace":
		p.refused = append(p.refused, fmt.Sprintf("%s: %s names the built-in profile %q in default_permissions, which this client does not know how to extend; nothing was installed for Codex", label, path, facts.defaultPermissions))
		planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)

		return codexConfigPlan{scope: codexNothing, left: true}
	case facts.hasDefault && facts.defaultPermissions == codexProfileName && !had:
		p.refused = append(p.refused, fmt.Sprintf("%s: %s names a profile of yours called %q, the name jevlin's own profile uses; nothing was installed for Codex. Rename yours and run this again", label, path, codexProfileName))
		planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)

		return codexConfigPlan{scope: codexNothing, left: true}
	case facts.proxy == proxyOther:
		p.refused = append(p.refused, fmt.Sprintf("%s: features.network_proxy in %s is a table without enabled = true or false, or a shape this client does not read, so whether Codex enforces a profile's host list cannot be told; nothing was installed for Codex. Set it to enabled = true and run this again", label, path))
		planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)

		return codexConfigPlan{scope: codexNothing, left: true}
	}

	// What Codex wrote inside our markers is moved out first, with our own
	// part as it stands, so a line of the participant's that the question
	// is about is edited where it will stay — commented out inside the
	// markers, it would read as one of our own comments and be lost.
	base := existing
	var moved codexRegionChange
	if had && !region.legacy && region.hasForeign() {
		norm, ch, why := installCodexRegion(existing, codexProfileRegion(region.profile()))
		if why != "" {
			p.refused = append(p.refused, fmt.Sprintf("%s: %s: %s", label, path, why))
			planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)

			return codexConfigPlan{scope: codexNothing, left: true}
		}
		base, moved = norm, ch
	}
	ed, err := newCodexEditor(string(base), entry.cfg)
	if err != nil {
		p.refused = append(p.refused, fmt.Sprintf("%s: %s: %v; nothing was written to it", label, path, err))
		planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)

		return codexConfigPlan{left: true}
	}

	// Which profile ours extends, and whether that one's network is
	// already open by the participant's own setting.
	switch {
	case had && facts.hasDefault && facts.defaultPermissions == codexProfileName:
		// An earlier yes: the marked line outside our region is the key,
		// and ours keeps extending what it extended then.
		profile.key = false
		profile.extends = region.extends
	case facts.hasDefault:
		profile.key = false
		profile.extends = facts.defaultPermissions
	}
	if profile.extends != ":workspace" {
		if !profileDefined(facts.doc, profile.extends) {
			if had && facts.hasDefault && facts.defaultPermissions == codexProfileName {
				// Ours already extends it, and it has gone since: Codex
				// refuses the file and does not start (seen live on
				// 0.158.0: "permissions profile `jevlin` extends undefined
				// profile"). Which profile ours should extend now is the
				// participant's to say.
				p.refused = append(p.refused, fmt.Sprintf("%s: jevlin's profile in %s extends your profile %s, which no [permissions] table defines any more, and Codex refuses the file and does not start until it does; nothing was installed for Codex. Put that profile back, or run agents uninstall, which puts your default_permissions line back, and set it to the profile you want", label, path, mustTOMLString(profile.extends)))
			} else {
				p.refused = append(p.refused, fmt.Sprintf("%s: %s names the profile %q in default_permissions, and no [permissions] table defines it; nothing was installed for Codex", label, path, profile.extends))
			}
			planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)

			return codexConfigPlan{scope: codexNothing, left: true}
		}
		if profileNetworkOn(facts.doc, profile.extends) && !facts.proxyOn() {
			// Their network is open by their own setting. Ours adds the
			// roots and nothing else: our hosts would mean nothing, and
			// turning the proxy on would cut their network down to ours.
			profile.network = false
			profile.proxy = false
		}
	}
	if facts.proxyOn() {
		profile.proxy = false
	}
	needProxyRewrite := profile.network && (facts.proxy == proxyBoolFalse || facts.proxy == proxyTableFalse)
	if needProxyRewrite {
		profile.proxy = false
	}
	if profile.network {
		// A search must reach the router: a wildcard deny of the
		// participant's that closes its host is refused before anything is
		// asked, because no yes would make the search work.
		if b, blocked := codexAllowedFor(facts.doc, profile.extends, profile.hosts).routerBlocked(cfg); blocked {
			p.refused = append(p.refused, fmt.Sprintf("%s: your profile %s in %s denies %s, which Codex applies over jevlin's allow of the router, %s, so a search from Codex could never reach it; nothing was installed for Codex. An exact allow beside the deny does not override it: narrow or remove the deny and run this again",
				label, mustTOMLString(b.profile), path, mustTOMLString(b.pattern), b.host))
			planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)
			return codexConfigPlan{scope: codexNothing, left: true}
		}
	}

	// The question, when the participant's own lines must change: one
	// question for all of them, showing everything that would be written.
	var changes []string // the participant's lines, as the question lists them
	switch {
	case facts.hasDefault && facts.defaultPermissions != codexProfileName:
		back := rootKeyRestores(ed.text, entry.cfg, "default_permissions", facts.defaultPermissions)
		changes = append(changes, fmt.Sprintf("default_permissions = %q becomes %q (marked; agents uninstall puts %q back)", facts.defaultPermissions, codexProfileName, back))
	case facts.hasSandboxMode: // "workspace-write", the only value left
		changes = append(changes, "sandbox_mode = \"workspace-write\" is commented out, since Codex does not combine it with a profile (marked; agents uninstall restores it)")
	}
	if needProxyRewrite {
		changes = append(changes, "network_proxy = false becomes true in your [features] table, so Codex enforces the profile's host list (marked; agents uninstall puts false back)")
	}
	if len(changes) > 0 {
		// The participant's lines, changed exactly as a yes changes them.
		apply := func(ed *codexEditor) error {
			var err error
			switch {
			case facts.hasDefault && facts.defaultPermissions != codexProfileName:
				err = ed.rewriteRootKey("default_permissions", mustTOMLString(codexProfileName), codexProfileName)
			case facts.hasSandboxMode:
				err = ed.commentOutRootKey("sandbox_mode")
			}
			if err == nil && needProxyRewrite {
				if facts.proxy == proxyBoolFalse {
					err = ed.rewriteInSection([]string{"features"}, "network_proxy", "true", true)
				} else {
					err = ed.rewriteInSection([]string{"features", "network_proxy"}, "enabled", "true", true)
				}
			}
			if err == nil {
				err = ed.verify()
			}
			return err
		}
		// The edit is tried before anything is asked: a change that cannot
		// be made is refused now, rather than after a yes, and the text
		// for a participant with no terminal is always the real one.
		trial, err := newCodexEditor(ed.text, entry.cfg)
		if err == nil {
			err = apply(trial)
		}
		if err != nil {
			planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)
			return codexRefuse(label, path, err, p)
		}
		answer := askConsent(ops, codexQuestion(label, path, ops.home, facts, profile, changes))
		switch answer {
		case consentNo:
			p.notes = append(p.notes, label+": nothing installed for Codex: you kept your own settings; the skill and hooks were not written")
			planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)
			return codexConfigPlan{scope: codexNothing}
		case consentAborted:
			p.aborted = promptAbortedReason
			planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)
			return codexConfigPlan{scope: codexNothing}
		case consentUnasked:
			p.unanswerable = true
			// The text is read after this run, which commits the safety
			// form of our own block: what it says to delete is what the
			// file will hold then.
			file := existing
			if safe, _, ok := codexSafeForm(existing, region, had, facts); ok {
				file = safe
			}
			byHand := codexByHand(string(file), ed.text, trial.text, profile, region, had)
			p.refused = append(p.refused, fmt.Sprintf("%s: %s needs a change to your own settings, which needs your yes at a terminal (-yes does not answer it); nothing was installed for Codex. By hand, with every line below copied exactly as printed, from its first character:\n%s",
				label, path, strings.TrimRight(byHand, "\n")))
			planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)
			return codexConfigPlan{scope: codexNothing, left: true}
		}
		if err := apply(ed); err != nil {
			planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)
			return codexRefuse(label, path, err, p)
		}
	}

	next, change, why := installCodexRegion([]byte(ed.text), codexProfileRegion(profile))
	if why != "" {
		p.refused = append(p.refused, fmt.Sprintf("%s: %s: %s", label, path, why))
		planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)
		return codexConfigPlan{scope: codexNothing, left: true}
	}
	change.movedRoots = append(moved.movedRoots, change.movedRoots...)
	change.movedTables = append(moved.movedTables, change.movedTables...)
	if string(next) == string(existing) {
		return codexConfigPlan{}
	}
	why = codexWriteWhy(profile, facts.doc)
	switch {
	case change.repaired:
		why = "permissions: repair jevlin's block, which had lost a marker (Codex deletes the comments above a table it removes) and would not have been found again; " + strings.TrimPrefix(why, "permissions: ")
	case change.migrated:
		why = "permissions: replace the sandbox block, which gave every Codex command open network, with " + strings.TrimPrefix(why, "permissions: ")
	case change.proxyRestored:
		why = fmt.Sprintf("permissions: restore [features.network_proxy], which had been turned off: without it every command Codex runs could reach any host; the profile's hosts, %s, are enforced again", joinLabels(hosts))
	}
	if len(changes) > 0 {
		why += "; your own settings changed as you agreed"
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
	changed, refused := planCodexWrite(ops, label, path, existing, next, participant, codexProfileHeaders(), mode, why, p)
	if refused {
		planCodexSafeForm(ops, label, path, existing, mode, region, had, facts, p)
		return codexConfigPlan{scope: codexNothing, left: true}
	}
	if changed {
		// The full write replaces our open block too, but it is the
		// participant's plan to accept; its safety form goes beside it, for
		// a run that commits nothing else.
		planCodexFallback(ops, label, path, existing, mode, region, had, facts, p)
	}
	return codexConfigPlan{changed: changed}
}

// codexQuestion is the one question install asks when the participant's
// own lines must change, with everything that would be written in it.
func codexQuestion(label, path, home string, facts codexFacts, profile codexProfile, changes []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s: %s\n", label, tilde(home, path))
	what := "jevlin's profile, which extends Codex's built-in \":workspace\" profile"
	if profile.extends != ":workspace" {
		what = fmt.Sprintf("jevlin's profile, which extends your profile %s", mustTOMLString(profile.extends))
	}
	adds := []string{"write access to " + joinLabels(profile.roots)}
	chain, blocked := "", ""
	if profile.network {
		allowed := codexAllowedFor(facts.doc, profile.extends, profile.hosts)
		adds = append(adds, "network access to "+allowed.sentence())
		if bs := allowed.blockedSentence(); bs != "" {
			blocked = bs + ".\n"
		}
		var names []string
		for _, n := range allowed.from {
			names = append(names, mustTOMLString(n))
		}
		switch {
		case allowed.every:
			chain = fmt.Sprintf("That is your profile %s's \"*\": Codex applies the domains of every profile in the extends chain once jevlin's profile turns the network on.\n", mustTOMLString(allowed.everyFrom))
		case len(names) == 1:
			chain = fmt.Sprintf("That list includes what your profile %s allows: Codex applies the domains of every profile in the extends chain once jevlin's profile turns the network on.\n", names[0])
		case len(names) > 1:
			chain = fmt.Sprintf("That list includes what your profiles %s allow: Codex applies the domains of every profile in the extends chain once jevlin's profile turns the network on.\n", joinLabels(names))
		}
		if profile.proxy {
			if allowed.every {
				// There is no host list to enforce; the table is what makes
				// the profile's domain rules apply at all.
				adds = append(adds, "the [features.network_proxy] table, through which Codex applies the profile's domain rules")
			} else {
				adds = append(adds, "the [features.network_proxy] table, which makes Codex enforce that host list")
			}
		}
	}
	fmt.Fprintf(&b, "Switch Codex to %s and adds %s?\n", what, strings.Join(adds, ", and "))
	b.WriteString(chain)
	b.WriteString(blocked)
	if !profile.network {
		fmt.Fprintf(&b, "Your profile's network is already open (network on, network_proxy off); jevlin leaves it so.\n")
	}
	b.WriteString("Your own settings change like this:\n")
	for _, c := range changes {
		b.WriteString("    " + c + "\n")
	}
	b.WriteString("jevlin's profile is written as its own block before your first table:\n")
	b.WriteString(indentBlock(codexProfileText(profile)) + "\n")
	b.WriteString("Switch? [y/N]: ")
	return b.String()
}

// codexByHand is what a participant with no terminal is told to do: the
// exact lines a yes would write, marks included, and our region with its
// markers. A file finished by hand that way is the file install would have
// written, so it reads as this installation's from then on — a later
// install refreshes it and uninstall takes it out and puts the changed
// lines back. Printed without the marks, it was a profile named "jevlin"
// that no marker claimed, which every later install refused as the
// participant's own.
//
// Every line is printed flush left, because it is copied as printed: a
// marker or a header indented by the report's own indentation is not one
// install recognizes. Where the file already has a block of ours — an
// installed region, or the old [sandbox_workspace_write] block — the text
// says what to do with it: a region of ours alone at the top is replaced
// where it stands; anything else (the old block below a table, a block
// that has lost a marker, one holding lines of the participant's) is
// deleted line for line as printed and the new block added before the
// first table, with the participant's own lines from it put back where
// install would put them.
func codexByHand(file, before, after string, profile codexProfile, region codexRegion, had bool) string {
	var b strings.Builder
	replaceInPlace := had && region.atTop && !region.legacy && !region.damaged && !region.hasForeign()
	// A line of the participant's that Codex wrote inside our block is put
	// back from it below, already changed as a yes changes it; a separate
	// "replace the line" for it would edit a line the next step deletes,
	// and the put-back would then restore the unchanged one.
	inBlock := map[string]bool{}
	if had && !replaceInPlace {
		for _, l := range strings.Split(region.foreignRoot+region.foreignText(), "\n") {
			inBlock[strings.TrimRight(l, "\r")] = true
		}
	}
	edited := map[string]string{}
	was := strings.Split(before, "\n")
	now := strings.Split(after, "\n")
	if len(was) == len(now) {
		for i := range was {
			w, n := strings.TrimRight(was[i], "\r"), strings.TrimRight(now[i], "\r")
			if w == n {
				continue
			}
			edited[w] = n
			if !inBlock[w] {
				fmt.Fprintf(&b, "replace the line\n%s\nwith\n%s\n", w, n)
			}
		}
	}
	putBack := func(text string) string {
		lines := strings.Split(strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), "\n")
		for i, l := range lines {
			if n, ok := edited[l]; ok {
				lines[i] = n
			}
		}
		return strings.Join(lines, "\n") + "\n"
	}
	block := string(codexProfileRegion(profile))
	// Codex reads the comments directly over a table as that table's, and
	// install puts the block above them (firstHeaderStart), so the text
	// says so rather than "before your first table" alone.
	first := "before your first table, above any comment lines directly over it"
	if profile.key {
		// Only a block that carries the key has a reason to be first.
		first += " (default_permissions must come before any table)"
	}
	switch {
	case !had:
		fmt.Fprintf(&b, "and add these lines, markers included, %s:\n%s", first, block)
	case replaceInPlace:
		fmt.Fprintf(&b, "and replace jevlin's block, from the line %s through the line %s, with these lines:\n%s", agentsMarkerBegin, agentsMarkerEnd, block)
	default:
		old := strings.ReplaceAll(file[len(region.pre):len(file)-len(region.post)], "\r\n", "\n")
		fmt.Fprintf(&b, "and delete jevlin's block, which is these lines:\n%s", withFinalNewline(old))
		fmt.Fprintf(&b, "then add these lines, markers included, %s:\n%s", first, block)
		if region.foreignRoot != "" {
			fmt.Fprintf(&b, "then put back these lines of yours from the deleted block, directly above the new one:\n%s", putBack(region.foreignRoot))
		}
		if t := region.foreignText(); t != "" {
			fmt.Fprintf(&b, "then put back these lines of yours from the deleted block, directly below the new one:\n%s", putBack(t))
		}
	}
	return b.String()
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

// planCodexWindowsRoots is Windows: the old table, without the network key,
// where no profile has been seen to work, written where it is found as the
// old block always was. The note says what the missing network costs.
func planCodexWindowsRoots(ops agentOps, label, path string, roots []string, entry binEntry, getenv func(string) string, p *agentPlan) (changed, left bool) {
	changed, left = planCodexSandbox(ops, label, path, roots, entry, getenv, p)
	if changed {
		p.notes = append(p.notes, label+": "+codexWindowsNetworkSentence)
		// The rewrite closes an old open block, but it is part of the plan
		// the participant accepts; the one-line close goes beside it for a
		// run that commits nothing else.
		if existing, mode, err := readCodexConfig(ops, path); err == nil {
			if region, had, why := readCodexRegion(existing); had && why == "" && region.legacy {
				planCodexFallback(ops, label, path, existing, mode, region, had, codexFacts{}, p)
			}
		}
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

// proxyOn: does the participant's file turn network_proxy on?
func (f codexFacts) proxyOn() bool { return f.proxy == proxyBoolTrue || f.proxy == proxyTableTrue }

// profileDefined: does doc define the profile name, or is it a built-in?
func profileDefined(doc tomlDoc, name string) bool {
	if strings.HasPrefix(name, ":") {
		return true
	}
	_, ok := lookupTOMLPath(doc, "permissions", name)
	return ok
}

// profileNetworkOn: does the profile name enable a network, as this file
// defines it? It follows extends through the file's own profiles, since a
// profile without a network table inherits its parent's; the built-ins
// ours can extend have none.
func profileNetworkOn(doc tomlDoc, name string) bool {
	for i := 0; i < 16 && !strings.HasPrefix(name, ":"); i++ {
		if v, ok := lookupTOMLPath(doc, "permissions", name, "network", "enabled"); ok {
			on, _ := v.(bool)
			return on
		}
		parent, _ := lookupTOMLPath(doc, "permissions", name, "extends")
		next, ok := parent.(string)
		if !ok || next == "" {
			return false
		}
		name = next
	}
	return false
}

// codexSafeForm is the file with our own region closed: the form it takes
// whenever install does not write the full profile, on every outcome — a
// typed no, an unanswered question, no terminal, a refusal. The question
// governs the participant's lines; our own block is ours, and the open
// network it may hold is what this client exists to close.
//
//   - The old [sandbox_workspace_write] block with network_access = true
//     keeps its writable roots and loses network_access, as Windows writes
//     it.
//   - Our profile with a network of its own and network_proxy off keeps its
//     roots and loses its network: with the proxy off, its host list opens
//     every host.
//
// ok is false when the region needs nothing.
func codexSafeForm(existing []byte, region codexRegion, had bool, facts codexFacts) (next []byte, why string, ok bool) {
	if !had {
		return nil, "", false
	}
	if region.legacy {
		next, ok = withoutOpenNetworkAccess(existing, region)
		if !ok {
			return nil, "", false
		}
		return next, "close the open network jevlin's old sandbox block gave every Codex command: remove its network_access = true line, and keep every other line of it as it is", true
	}
	if !region.network || region.proxyPresent || facts.proxyOn() {
		return nil, "", false
	}
	prof := region.profile()
	prof.network, prof.hosts = false, nil
	next, _, refusal := installCodexRegion(existing, codexProfileRegion(prof))
	if refusal != "" {
		return nil, "", false
	}
	return next, "close the network jevlin's profile would open while network_proxy is off: keep its writable roots, drop its network", true
}

// planCodexSafeForm plans codexSafeForm as a safety write, and says so.
func planCodexSafeForm(ops agentOps, label, path string, existing []byte, mode os.FileMode, region codexRegion, had bool, facts codexFacts, p *agentPlan) {
	next, why, ok := codexSafeForm(existing, region, had, facts)
	if !ok {
		return
	}
	if changed, _ := planCodexWrite(ops, label, path, existing, next, region.participantInPlace(), nil, mode, why, p); changed {
		p.writes[len(p.writes)-1].safety = true
		p.notes = append(p.notes, label+": "+why+"; this is jevlin's own block, whatever the answer about your settings")
	}
}

// codexAllowed is what a profile of ours allows once Codex merges it with
// the profiles it extends. Codex resolves each domain key by its nearest
// definition — ours first, then up the extends chain — and "*" the same
// way, on its own: a host is reachable when its own key resolves to allow,
// or when "*" resolves to allow and its own key does not resolve to deny.
// Seen live on 0.158.0: a parent's deny under a child's allow is open, a
// parent's allow under a child's deny is closed, a child's "*" over a
// parent's deny of example.com still closes example.com, and a parent's
// "*" under a child's deny of it opens every host but that one. Codex
// applies the chain's domains as soon as ours turns the network on,
// whatever the chain's own `enabled` says.
type codexAllowed struct {
	hosts     []string // keys that resolve to allow, ours included, sorted
	every     bool     // "*" resolves to allow
	everyFrom string   // the profile whose "*" that is
	except    []string // with every: the keys that resolve to deny, sorted
	from      []string // the chain's profiles whose allows are in effect
	// blocked is the hosts allowed by exact key, ours included, that a
	// wildcard deny the chain resolves to closes all the same: Codex
	// applies a matching "*." or "**." deny over any exact allow, at any
	// level, the nearer included (seen live on 0.158.0).
	blocked []codexBlock
}

// codexBlock is one allowed host a wildcard deny closes.
type codexBlock struct{ host, pattern, profile string }

// wildcardDenies: does a deny key match host as Codex matches it? Seen
// live on 0.158.0, through `codex sandbox`: a key holding "*" or "?" is a
// glob over the whole host name, "*" any run of characters, dots and none
// included, and "?" one character ("*example.com", "www.example.*",
// "w*.example.com", "*.example.co?" all close www.example.com); "*.d" is
// every subdomain of d at any depth and not d, which the glob says by
// itself, and "**.d" is those and d as well. Case is ignored, and so is one
// trailing dot, on the key and on the host.
func wildcardDenies(pattern, host string) bool {
	pattern, host = codexHostKey(pattern), codexHostKey(host)
	if d, ok := strings.CutPrefix(pattern, "**."); ok && host == d {
		return true
	}
	return globMatch(pattern, host)
}

// isDenyGlob: is a domain key one Codex reads as a pattern? A bare "*" is
// not among them: Codex refuses it as a deny, and as an allow it is every
// host, which codexAllowedFor reads on its own.
func isDenyGlob(key string) bool {
	return key != "*" && strings.ContainsAny(key, "*?")
}

// codexHostKey is a host name or domain key as Codex compares it.
func codexHostKey(h string) string {
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// globMatch matches s against a pattern of "*" (any run, possibly empty)
// and "?" (one byte); every other byte stands for itself.
func globMatch(pattern, s string) bool {
	p, i := 0, 0
	star, mark := -1, 0
	for i < len(s) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == s[i]):
			p++
			i++
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case star >= 0:
			p = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

func codexAllowedFor(doc tomlDoc, extends string, ours []string) codexAllowed {
	rule := map[string]string{} // key -> its nearest definition
	by := map[string]string{}   // key -> the profile that defined it
	for _, h := range ours {
		rule[strings.ToLower(h)] = "allow"
	}
	name := extends
	for i := 0; i < 16 && name != "" && !strings.HasPrefix(name, ":"); i++ {
		if v, ok := lookupTOMLPath(doc, "permissions", name, "network", "domains"); ok {
			if table, ok := v.(map[string]any); ok {
				level := map[string]string{}
				for h, r := range table {
					h = strings.ToLower(h)
					if r, ok := r.(string); ok && (r == "allow" || r == "deny") && level[h] != "allow" {
						// Two spellings of one host in one profile: report
						// the open one, which is the one to warn about.
						level[h] = r
					}
				}
				for h, r := range level {
					if _, nearer := rule[h]; !nearer {
						rule[h], by[h] = r, name
					}
				}
			}
		}
		parent, _ := lookupTOMLPath(doc, "permissions", name, "extends")
		name, _ = parent.(string)
	}
	var a codexAllowed
	var denies []string
	for h, r := range rule {
		if r == "deny" && isDenyGlob(h) {
			denies = append(denies, h)
		}
	}
	sort.Strings(denies)
	from := map[string]bool{}
	for h, r := range rule {
		switch {
		case h == "*":
			// Codex refuses "*" = "deny" ("invalid value for
			// network.denied_domains: *"), so a file it runs never has
			// one; the check stays because install and status read files
			// Codex has not validated, and a deny read as an allow would
			// call every host open.
			if r == "allow" {
				a.every, a.everyFrom = true, by[h]
			}
		case r == "allow":
			if i := slices.IndexFunc(denies, func(d string) bool { return wildcardDenies(d, h) }); i >= 0 {
				a.blocked = append(a.blocked, codexBlock{host: h, pattern: denies[i], profile: by[denies[i]]})
				continue
			}
			a.hosts = append(a.hosts, h)
			if by[h] != "" {
				from[by[h]] = true
			}
		default:
			a.except = append(a.except, h)
		}
	}
	sort.Slice(a.blocked, func(i, j int) bool { return a.blocked[i].host < a.blocked[j].host })
	for n := range from {
		a.from = append(a.from, n)
	}
	sort.Strings(a.from)
	a.hosts = cleanHosts(a.hosts)
	if !a.every {
		a.except = nil
	}
	a.except = cleanHosts(a.except)
	return a
}

// blockedSentence names the allowed hosts a wildcard deny closes, or is
// empty.
func (a codexAllowed) blockedSentence() string {
	if len(a.blocked) == 0 {
		return ""
	}
	var parts []string
	for _, b := range a.blocked {
		parts = append(parts, fmt.Sprintf("%s, which your profile %s denies with %s", b.host, mustTOMLString(b.profile), mustTOMLString(b.pattern)))
	}
	return "Codex blocks " + strings.Join(parts, "; ") + ": a matching wildcard deny wins over any exact allow, jevlin's included"
}

// routerBlocked is the block that closes the router's host, if one does.
func (a codexAllowed) routerBlocked(cfg *config.Config) (codexBlock, bool) {
	if cfg == nil || cfg.Miner.RouterURL == nil {
		return codexBlock{}, false
	}
	router := codexHostKey(cfg.Miner.RouterURL.Hostname())
	for _, b := range a.blocked {
		if codexHostKey(b.host) == router {
			return b, true
		}
	}
	return codexBlock{}, false
}

// sentence is the allowed network in words: every host, or the list.
func (a codexAllowed) sentence() string {
	if a.every {
		but := ""
		if len(a.except) > 0 {
			but = " but " + joinLabels(a.except)
		}
		return fmt.Sprintf("every host%s, because your profile %s allows \"*\"", but, mustTOMLString(a.everyFrom))
	}
	return joinLabels(a.hosts) + " only"
}

// legacyNetworkOpen: does our old block's own table set network_access to
// true? Present and false is closed, and is left alone.
func legacyNetworkOpen(region codexRegion) bool {
	v, _ := lookupTOMLPath(mustDecode(region.oursText()), codexSandboxTable, "network_access")
	on, _ := v.(bool)
	return on
}

// withoutOpenNetworkAccess is the file with the one `network_access = true`
// line of our old block taken out, and nothing else changed: the block's
// other keys — writable_roots, and exclude_slash_tmp or
// exclude_tmpdir_env_var a participant may have added, which keep /tmp out
// of the sandbox's writable set — stay byte for byte. Re-rendering the
// block from its roots dropped them silently, and with them a /tmp that
// had been read-only became writable (seen live under codex exec on
// 0.158.0). The result is held to the decoded file minus exactly that key.
func withoutOpenNetworkAccess(existing []byte, region codexRegion) ([]byte, bool) {
	if !legacyNetworkOpen(region) {
		return nil, false
	}
	s := string(existing)
	from := len(region.pre)
	to := from + len(agentsMarkerBegin) + len(region.region)
	var hit *textLine
	for _, l := range linesOutsideTOMLStrings(s) {
		if l.start < from || l.end > to {
			continue
		}
		// A CRLF file's line still ends in "\r" here, which TOML does not
		// take as the end of a value; read without it, delete through "\n".
		doc, ok := decodeTOMLDoc(strings.TrimRight(s[l.start:l.end], "\r"))
		if ok && len(doc) == 1 && doc["network_access"] == true {
			if hit != nil {
				return nil, false
			}
			l := l
			hit = &l
		}
	}
	if hit == nil {
		return nil, false
	}
	end := hit.end
	if end < len(s) && s[end] == '\n' {
		end++
	}
	next := s[:hit.start] + s[end:]
	before, ok1 := decodeTOMLDoc(s)
	after, ok2 := decodeTOMLDoc(next)
	if !ok1 || !ok2 {
		return nil, false
	}
	deleteTOMLPath(before, codexSandboxTable, "network_access")
	if !tomlDocsEqual(before, after) {
		return nil, false
	}
	return []byte(next), true
}

// planCodexFallback puts codexSafeForm in p.fallback: committed only when
// the plan as a whole is not.
func planCodexFallback(ops agentOps, label, path string, existing []byte, mode os.FileMode, region codexRegion, had bool, facts codexFacts, p *agentPlan) {
	next, why, ok := codexSafeForm(existing, region, had, facts)
	if !ok || codexTOMLError(string(next)) != nil {
		return
	}
	if disk, err := ops.readFile(path); err == nil && strings.HasPrefix(string(disk), codexBOM) {
		next = append([]byte(codexBOM), next...)
	}
	p.fallback = append(p.fallback, agentWrite{surface: label, path: path, contents: next, mode: mode, why: why, safety: true})
}
