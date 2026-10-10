package main

// The marked region in Codex's config.toml: what is in it, whose each line
// is, where it goes, and the net under every edit.
//
// The region holds a bare key, `default_permissions`, which TOML requires
// before the first table header, so the region sits BEFORE THE FIRST HEADER
// of the file — one region, inserted above the comment run attached to that
// header, or at the end of a file that has no table. One region rather than
// a key at the top and a block wherever it was, because the file's first
// header is a position the file itself defines: uninstall then install is
// byte-identical for every file, and one decode covers key and tables.
//
// Codex writes into the region. Seen live (Codex 0.158.0 Linux and
// 0.160.0 macOS, the files are in testdata/codex/): app-server's
// config/value/write puts a root key directly after our default_permissions,
// inside the markers; `codex features enable` with no [features] table of
// the participant's writes `[features]` directly above our
// [features.network_proxy]. And `codex features disable network_proxy`
// DELETES our [features.network_proxy] table, leaving the profile open
// to every host. So what is between the markers is split by who wrote it,
// one level finer than before: a root key that is not default_permissions
// moves to just above the region, a table that is not one of ours to just
// below it, and nothing of anybody's is deleted. That holds for install and
// for uninstall alike, through one reading (readCodexRegion).

import (
	"fmt"
	"strings"
)

// codexRegion is a marked region split by who wrote each part.
type codexRegion struct {
	pre, region, post string

	legacy bool // ours is the old [sandbox_workspace_write] table
	atTop  bool // nothing before the region is a table header

	ourKey      string        // the default_permissions line, "" when absent
	ourComments string        // comment and blank lines of the preamble: ours, dropped with the region
	ours        []tomlSection // our tables in file order
	foreignRoot string        // root-key lines in the preamble that are not ours, their own bytes
	foreign     []tomlSection // tables that are not ours, their own bytes

	// what ours decodes to
	roots, hosts []string
	extends      string
	keyPresent   bool
	proxyPresent bool
	network      bool // our profile has a network table of its own
}

// profile is what our part renders from, as read.
func (r codexRegion) profile() codexProfile {
	return codexProfile{roots: r.roots, hosts: r.hosts, extends: r.extends, key: r.keyPresent, network: r.network, proxy: r.proxyPresent}
}

func (r codexRegion) oursText() string {
	var b strings.Builder
	b.WriteString(r.ourComments)
	b.WriteString(r.ourKey)
	for _, s := range r.ours {
		b.WriteString(s.text)
	}
	return b.String()
}

func (r codexRegion) foreignText() string {
	var b strings.Builder
	for _, s := range r.foreign {
		b.WriteString(s.text)
	}
	return b.String()
}

func (r codexRegion) foreignNames() []string {
	out := make([]string, 0, len(r.foreign))
	for _, s := range r.foreign {
		out = append(out, "["+s.header+"]")
	}
	return out
}

// foreignRootNames are the keys the moved root lines define, for the plan.
func (r codexRegion) foreignRootNames() []string {
	doc, ok := decodeTOMLDoc(r.foreignRoot)
	if !ok {
		return nil
	}
	return sortedKeys(doc)
}

func (r codexRegion) hasForeign() bool { return r.foreignRoot != "" || len(r.foreign) > 0 }

// participantInPlace is the file with only our lines taken out and every
// other byte where it stood: what the participant's own content means, which
// no edit may change. A bare key of ours below a table header decodes as
// that table's, so the whole file is not the reference; this is.
func (r codexRegion) participantInPlace() string {
	return r.pre + r.foreignRoot + r.foreignText() + r.post
}

// readCodexRegion reads the marked region of file. had is false when there
// is none; why is non-empty when there is one that cannot be acted on, and
// the caller then leaves the file exactly as it is and says so.
func readCodexRegion(file []byte) (r codexRegion, had bool, why string) {
	pre, region, post, ok := markedRegion(file)
	if !ok {
		return codexRegion{}, false, ""
	}
	r = codexRegion{pre: pre, region: region, post: post, atTop: !holdsHeaderLine(pre)}
	preamble, sections, ok := splitMarkedBlock(region)
	if !ok {
		return r, true, "it cannot be read as TOML tables, so which of them are ours cannot be decided; remove it by hand"
	}
	if why := r.splitPreamble(preamble); why != "" {
		return r, true, why
	}
	profile := map[string]bool{}
	for _, h := range codexProfileHeaders() {
		profile[h] = true
	}
	for _, s := range sections {
		name := strings.Map(func(c rune) rune {
			if c == ' ' || c == '\t' {
				return -1
			}
			return c
		}, s.header)
		switch {
		case profile[name]:
			r.ours = append(r.ours, s)
		case name == codexSandboxTable:
			r.ours = append(r.ours, s)
			r.legacy = true
		default:
			r.foreign = append(r.foreign, s)
		}
	}
	if r.legacy && len(r.ours) > 1 {
		return r, true, "it holds both the old [" + codexSandboxTable + "] table and a permission profile, which this client never writes together; remove the block by hand and run this again"
	}
	if r.legacy && !r.atTop && r.foreignRoot != "" {
		// The old block sat anywhere, so bare keys before its first table
		// belong to whatever table preceded the block; moving them would
		// re-parent them (dropin-miner#82's reading, unchanged).
		return r, true, "it holds keys before its first table that belong to the table above the block; remove the block by hand and run this again"
	}
	// The net, in both directions: what is about to be treated as ours
	// must decode to exactly what the renderer writes, and what is about to
	// be kept as somebody else's must not define any key of ours.
	if why := r.readOurs(); why != "" {
		return r, true, why
	}
	for _, s := range r.foreign {
		for _, p := range codexOurPaths() {
			if definesPath(s.text, p...) {
				return r, true, fmt.Sprintf("a table inside it that jevlin did not write, [%s], defines %s; remove the block by hand and run this again", s.header, strings.Join(p, "."))
			}
		}
	}
	if r.foreignRoot != "" && definesPath(r.foreignRoot, "default_permissions") {
		return r, true, "a line inside it that jevlin did not write sets default_permissions; remove the block by hand and run this again"
	}
	return r, true, ""
}

// splitPreamble separates the lines before the region's first table: our
// comments, our one key, and root keys Codex or a person wrote there.
func (r *codexRegion) splitPreamble(preamble string) string {
	whole, ok := decodeTOMLDoc(preamble)
	if !ok {
		return "it holds lines before its first table that do not read as TOML; remove the block by hand and run this again"
	}
	var comments, foreign strings.Builder
	for _, line := range strings.SplitAfter(preamble, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			comments.WriteString(line)
			continue
		}
		if doc, ok := decodeTOMLDoc(line); ok && len(doc) == 1 {
			if _, isKey := doc["default_permissions"]; isKey {
				if r.ourKey != "" {
					return "it sets default_permissions twice; remove the block by hand and run this again"
				}
				r.ourKey = line
				continue
			}
		}
		foreign.WriteString(line)
	}
	r.ourComments = comments.String()
	r.foreignRoot = foreign.String()
	// The line split must agree with the decode: a value that spans lines
	// would be cut, and the cut would decode to something else or not at
	// all.
	expected := copyTOMLDoc(whole)
	delete(expected, "default_permissions")
	got, ok := decodeTOMLDoc(r.foreignRoot)
	if !ok || !tomlDocsEqual(expected, got) {
		return "it holds lines before its first table that cannot be separated line by line; remove the block by hand and run this again"
	}
	if r.ourKey != "" {
		if v, _ := whole["default_permissions"].(string); v != codexProfileName {
			return fmt.Sprintf("its default_permissions names %q, not jevlin's profile; remove the block by hand and run this again", v)
		}
	}
	return ""
}

// readOurs reads roots and hosts out of our tables and holds them to the
// renderer: decoded, our part must be exactly what codexProfileText writes
// for those roots and hosts. Anything else — a value changed by hand, a
// table nested under our name that the scan missed — means the text is not
// ours to rewrite or delete, and nothing is touched.
func (r *codexRegion) readOurs() string {
	text := r.ourKey
	for _, s := range r.ours {
		text += s.text
	}
	if r.legacy {
		if !oursIsOnlyOurs(text) {
			return "the [" + codexSandboxTable + "] table inside it is not only jevlin's; remove the block by hand and run this again"
		}
		r.roots = markedSandboxRoots(text)
		return ""
	}
	doc, ok := decodeTOMLDoc(text)
	if !ok {
		return "its own tables do not read as TOML; remove the block by hand and run this again"
	}
	if len(r.ours) == 0 {
		if r.ourKey == "" {
			return "it holds nothing of jevlin's; remove the block by hand and run this again"
		}
		return "it holds default_permissions but no profile; remove the block by hand and run this again"
	}
	_, r.keyPresent = doc["default_permissions"]
	_, r.proxyPresent = lookupTOMLPath(doc, "features", "network_proxy")
	_, r.network = lookupTOMLPath(doc, "permissions", codexProfileName, "network")
	if v, ok := lookupTOMLPath(doc, "permissions", codexProfileName, "extends"); ok {
		r.extends, _ = v.(string)
	}
	r.roots = keysWithValue(doc, "write", "permissions", codexProfileName, "filesystem")
	r.hosts = keysWithValue(doc, "allow", "permissions", codexProfileName, "network", "domains")
	rendered, _ := decodeTOMLDoc(codexProfileText(r.profile()))
	if !tomlDocsEqual(doc, rendered) {
		return "its profile is not exactly what jevlin writes (a value was changed or a key added inside it); remove the block by hand and run this again"
	}
	return ""
}

// keysWithValue lists the keys of the table at path whose value is want,
// sorted, with a sentinel entry when a value is something else so the
// renderer comparison fails.
func keysWithValue(doc tomlDoc, want string, path ...string) []string {
	v, ok := lookupTOMLPath(doc, path...)
	if !ok {
		return nil
	}
	table, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	var out []string
	for k, val := range table {
		if s, _ := val.(string); s == want {
			out = append(out, k)
		}
	}
	return cleanStrings(out)
}

func cleanStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sortStrings(out)
	return out
}

// definesPath: does this text, decoded, define the key at path?
func definesPath(text string, path ...string) bool {
	doc, ok := decodeTOMLDoc(text)
	if !ok {
		return true // unreadable is not provably free of it
	}
	_, found := lookupTOMLPath(doc, path...)
	return found
}

// holdsHeaderLine: does any line of s's own structure read as a table
// header? A line inside a multi-line string is not one.
func holdsHeaderLine(s string) bool {
	return firstHeaderLine(s) >= 0
}

// firstHeaderLine is the index into linesOutsideTOMLStrings(s) of the first
// table header, or -1.
func firstHeaderLine(s string) int {
	for i, l := range linesOutsideTOMLStrings(s) {
		if tomlHeaderLine.MatchString(strings.TrimRight(s[l.start:l.end], "\r")) {
			return i
		}
	}
	return -1
}

// firstHeaderStart is the byte offset where the region goes: the start of
// the comment run attached to the file's first table header, or -1 when the
// file has no header. Lines inside a multi-line string are neither headers
// nor comments.
func firstHeaderStart(s string) int {
	lines := linesOutsideTOMLStrings(s)
	header := firstHeaderLine(s)
	if header < 0 {
		return -1
	}
	start := header
	for start > 0 {
		prev := lines[start-1]
		if prev.end+1 != lines[start].start {
			break // a string ends between them: not one comment run
		}
		t := strings.TrimSpace(s[prev.start:prev.end])
		if t != "" && !strings.HasPrefix(t, "#") {
			break
		}
		start--
	}
	// A blank run between the last root key and the header's own comment
	// stays with the root keys: the region takes the header's comments,
	// not the gap above them.
	for start < header && strings.TrimSpace(s[lines[start].start:lines[start].end]) == "" {
		start++
	}
	return lines[start].start
}

// insertBeforeFirstHeader puts block before the file's first table header,
// or at the end of a file that has none, and adds no byte of its own: the
// block is spliced in at a line boundary, so taking exactly its bytes out
// again (removeCodexRegion) gives back the participant's file byte for
// byte. The one byte it may add is the newline a file without a final one
// needs before the block can start on a line of its own, which uninstall
// cannot know it added.
func insertBeforeFirstHeader(file string, block []byte) []byte {
	idx := firstHeaderStart(file)
	if idx < 0 {
		return []byte(withFinalNewline(file) + string(block))
	}
	return []byte(file[:idx] + string(block) + file[idx:])
}

func withFinalNewline(s string) string {
	if s != "" && !strings.HasSuffix(s, "\n") {
		return s + "\n"
	}
	return s
}


// codexRegionChange is what installing the region did to the file, for the
// plan's sentences.
type codexRegionChange struct {
	unchanged     bool     // the region already reads as the renderer writes it
	migrated      bool     // the old [sandbox_workspace_write] block was replaced
	proxyRestored bool     // our [features.network_proxy] table was missing and is back
	movedRoots    []string // root keys moved to just above the region
	movedTables   []string // tables moved below the region
	legacyRoots   []string // the old block's roots, for attribution before the write
	droppedKeys   []string // keys inside the old table the renderer does not write
}

// installCodexRegion is the file with want as its region. Whatever was in
// the markers that is not ours is kept; nothing of anybody's moves relative
// to anything else of theirs; and the result decodes to the file before plus
// exactly what want decodes to, or the edit is refused.
func installCodexRegion(existing []byte, want []byte) (next []byte, change codexRegionChange, why string) {
	r, had, why := readCodexRegion(existing)
	if had && why != "" {
		return nil, change, why
	}
	wantInner := mustRegion(want)
	var candidates [][]byte
	switch {
	case !had:
		candidates = append(candidates, insertBeforeFirstHeader(string(existing), want))
	case r.legacy || !r.atTop:
		change.migrated = r.legacy
		change.legacyRoots = r.roots
		change.droppedKeys = keysWeDidNotWrite(r.oursText())
		change.movedTables = r.foreignNames()
		stripped, _ := removeMarkedBlock(existing)
		if len(r.foreign) > 0 {
			stripped = appendTables(stripped, r.foreignText())
		}
		candidates = append(candidates, insertBeforeFirstHeader(string(stripped), want))
	default:
		if !r.hasForeign() && r.region == wantInner {
			change.unchanged = true
			return existing, change, ""
		}
		change.proxyRestored = !r.proxyPresent && definesPath(wantInner, "features", "network_proxy")
		change.movedRoots = r.foreignRootNames()
		change.movedTables = r.foreignNames()
		// Root keys just above, tables just below; and, should a line of
		// the participant's own after the end marker make "just below"
		// re-parent it, the tables go to the end of the file instead,
		// where nothing can follow them.
		candidates = append(candidates,
			[]byte(r.pre+r.foreignRoot+string(want)+r.foreignText()+r.post),
			appendTables([]byte(r.pre+r.foreignRoot+string(want)+r.post), r.foreignText()),
		)
	}
	reference := string(existing)
	if had {
		reference = r.participantInPlace()
	}
	expected, ok := decodeTOMLDoc(reference)
	if !ok {
		return nil, change, "the file does not read as TOML, so the profile cannot be added to it safely"
	}
	added, _ := decodeTOMLDoc(wantInner)
	deepMergeTOML(expected, added)
	for _, c := range candidates {
		if got, ok := decodeTOMLDoc(string(c)); ok && tomlDocsEqual(expected, got) {
			return c, change, ""
		}
	}
	return nil, change, "writing the profile would change the meaning of something else in the file (a key would end up under another table), so nothing was written; add the profile by hand"
}

// codexRegionRemoval is what uninstall concluded about the region.
type codexRegionRemoval struct {
	next  []byte
	had   bool
	ours  bool // may be written; false with why set, or when the region is another installation's
	kept  []string
	why   string
	roots []string // what the region grants, for attribution
	// dropped is the keys inside the legacy table the renderer does not
	// write, named before they go.
	dropped []string
}

// removeCodexRegion takes out exactly our lines. A root key or a table that
// is not ours stays: the root keys where the region was, which is still
// before the first header, and the tables at the end of the file.
func removeCodexRegion(existing []byte) codexRegionRemoval {
	r, had, why := readCodexRegion(existing)
	if !had {
		return codexRegionRemoval{next: existing}
	}
	if why != "" {
		return codexRegionRemoval{next: existing, had: true, why: why}
	}
	// Exactly the region's bytes come out, and nothing else moves: install
	// spliced it in without a byte of its own, so this is its inverse.
	next := []byte(r.pre + r.foreignRoot + r.post)
	if len(r.foreign) > 0 {
		next = appendTables(next, r.foreignText())
	}
	// The reference is the file as it was, decoded, minus exactly the keys
	// our own part defines: anything else that would change — a string
	// that happened to hold our text, a key the line work misplaced — is a
	// refusal. A region below a table header is the one exception, since
	// its bare key decodes there as that table's; it is compared in place.
	var expected tomlDoc
	var ok bool
	if r.atTop || r.legacy {
		var whole, ours tomlDoc
		if whole, ok = decodeTOMLDoc(string(existing)); ok {
			if ours, ok = decodeTOMLDoc(r.oursText()); ok {
				expected = subtractDoc(whole, ours)
			}
		}
	} else {
		expected, ok = decodeTOMLDoc(r.participantInPlace())
	}
	if !ok {
		return codexRegionRemoval{next: existing, had: true, roots: r.roots, why: "the file does not read as TOML, so the region cannot be taken out of it safely; remove it by hand"}
	}
	got, ok := decodeTOMLDoc(string(next))
	if !ok || !tomlDocsEqual(expected, got) {
		return codexRegionRemoval{next: existing, had: true, roots: r.roots, why: "taking the region out would change the meaning of something else in the file; remove it by hand"}
	}
	out := codexRegionRemoval{next: next, had: true, ours: true, kept: r.foreignNames(), roots: r.roots}
	if r.legacy {
		out.dropped = keysWeDidNotWrite(r.oursText())
	}
	return out
}
