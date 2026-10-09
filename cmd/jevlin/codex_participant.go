package main

// Edits to lines of the participant's own in Codex's config.toml, and their
// undoing.
//
// Most of what this client writes into config.toml lives between its two
// markers. Three decisions put a line of ours into a table that is not ours:
// a profile of the participant's that default_permissions already names gets
// jevlin's entries added to it (D3); a value of theirs that would otherwise
// fight the profile — `[features] network_proxy = false`,
// `default_permissions = ":workspace"` — is rewritten (D4, C5); and a bare
// `sandbox_mode = "workspace-write"` is commented out (C5). Each such line
// carries a trailing comment naming the installation that wrote it, by its
// config path, which is ownership_match.go's rule applied one line at a time;
// and each is written only after the participant typed yes to a question
// that showed the lines. Uninstall takes back exactly the lines carrying
// this installation's mark — deleting an added one, restoring a rewritten
// or commented-out one — and nothing else, under the same decoded-document
// net every other edit here is under.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
)

// profileEntries is what a participant's profile is missing for the search
// to work: C4's two cases. network is false in case (ii), a network already
// on with the proxy off, where only the filesystem lines are added so the
// participant's open network is not cut down to our hosts.
type profileEntries struct {
	name    string
	roots   []string // filesystem entries to add
	enable  bool     // network.enabled must become true
	rewrite bool     // ...by rewriting an existing false (else by adding the key)
	hosts   []string // domain entries to add
	proxy   bool     // the [features.network_proxy] table must be added
	proxyFx bool     // ...or [features] network_proxy = false rewritten to true
}

func (e profileEntries) empty() bool {
	return len(e.roots) == 0 && !e.enable && len(e.hosts) == 0 && !e.proxy && !e.proxyFx
}

// missingProfileEntries compares the participant's profile with what the
// search needs.
func missingProfileEntries(f codexFacts, roots, hosts []string) profileEntries {
	e := profileEntries{name: f.defaultPermissions}
	have := keysWithValue(f.doc, "write", "permissions", e.name, "filesystem")
	for _, r := range roots {
		if !dirsInclude(have, r) {
			e.roots = append(e.roots, r)
		}
	}
	netEnabled, netPresent := lookupTOMLPath(f.doc, "permissions", e.name, "network", "enabled")
	on, _ := netEnabled.(bool)
	proxyOn := f.proxy == proxyBoolTrue || f.proxy == proxyTableTrue
	if netPresent && on && !proxyOn {
		// C4 (ii): their network is unrestricted; the domain lines would
		// mean nothing, and turning the proxy on would cut their network
		// down to our hosts.
		return e
	}
	if !on {
		e.enable = true
		e.rewrite = netPresent
	}
	allowed := keysWithValue(f.doc, "allow", "permissions", e.name, "network", "domains")
	for _, h := range hosts {
		if !containsString(allowed, h) {
			e.hosts = append(e.hosts, h)
		}
	}
	switch f.proxy {
	case proxyUnset:
		e.proxy = true
	case proxyBoolFalse:
		e.proxyFx = true
	}
	return e
}

// entriesText renders the entries as the participant is shown them, and as
// they are written (without the marks).
func (e profileEntries) text() string {
	var b strings.Builder
	if len(e.roots) > 0 {
		b.WriteString("[permissions." + e.name + ".filesystem]\n")
		for _, r := range e.roots {
			b.WriteString(mustTOMLString(r) + " = \"write\"\n")
		}
	}
	if e.enable {
		b.WriteString("[permissions." + e.name + ".network]\nenabled = true\n")
	}
	if len(e.hosts) > 0 {
		b.WriteString("[permissions." + e.name + ".network.domains]\n")
		for _, h := range e.hosts {
			b.WriteString(mustTOMLString(h) + " = \"allow\"\n")
		}
	}
	if e.proxy {
		b.WriteString("[features.network_proxy]\nenabled = true\n")
	}
	if e.proxyFx {
		b.WriteString("[features]\nnetwork_proxy = true\n")
	}
	return b.String()
}

// ── the editor ──────────────────────────────────────────────────────────

// setTOMLPath writes value at path, creating tables on the way. A non-table
// in the way is replaced, which the net then reports as a difference if the
// text edit did not do the same.
func setTOMLPath(doc tomlDoc, value any, path ...string) {
	for _, k := range path[:len(path)-1] {
		child, ok := doc[k].(map[string]any)
		if !ok {
			child = map[string]any{}
			doc[k] = child
		}
		doc = child
	}
	doc[path[len(path)-1]] = value
}

// codexEditor applies marked edits to a file's text and keeps, beside it,
// the decoded document the result must equal.
type codexEditor struct {
	text     string
	expected tomlDoc
	cfgPath  string
}

func newCodexEditor(text, cfgPath string) (*codexEditor, error) {
	doc, ok := decodeTOMLDoc(text)
	if !ok {
		return nil, errors.New("the file does not read as TOML")
	}
	return &codexEditor{text: text, expected: doc, cfgPath: cfgPath}, nil
}

// verify is the net: the text must decode to exactly what the edits meant.
func (ed *codexEditor) verify() error {
	got, ok := decodeTOMLDoc(ed.text)
	if !ok || !tomlDocsEqual(ed.expected, got) {
		return errors.New("the edit would change the meaning of something else in the file, so nothing was written")
	}
	return nil
}

func (ed *codexEditor) mark(suffix string) (string, error) { return codexMark(ed.cfgPath, suffix) }

// keyLineIn finds the one line of lines that, read alone, sets key.
func keyLineIn(lines []string, key string) (int, error) {
	found := -1
	for i, line := range lines {
		doc, ok := decodeTOMLDoc(line)
		if !ok || len(doc) != 1 {
			continue
		}
		if _, is := doc[key]; !is {
			continue
		}
		if found >= 0 {
			return -1, fmt.Errorf("%s is set on more than one line", key)
		}
		found = i
	}
	if found < 0 {
		return -1, fmt.Errorf("no line sets %s", key)
	}
	return found, nil
}

// sectionSpan is where a table's lines are in the file: the header line and
// the lines of its body, as indexes into the file's lines.
type sectionSpan struct {
	header, end int // lines[header] is the header; body is lines[header+1:end]
}

// findSection locates the table named by path in text, by the same scan
// splitMarkedBlock uses. ok is false when the file cannot be split cleanly
// or the table is not there.
func findSection(text string, path []string) (lines []string, span sectionSpan, found bool, err error) {
	lines = strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	preamble, sections, ok := splitMarkedBlock(text)
	if !ok {
		return nil, span, false, errors.New("the file cannot be read as TOML tables")
	}
	// Walk the sections in order to map each onto line indexes, starting
	// after the lines that precede the first table.
	at := strings.Count(preamble, "\n")
	for _, s := range sections {
		n := strings.Count(s.text, "\n")
		if !strings.HasSuffix(s.text, "\n") {
			n++
		}
		start := at
		at += n
		if !samePathSegments(headerPath(s.header), path) {
			continue
		}
		header := start
		for header < at && !tomlHeaderLine.MatchString(strings.TrimRight(lines[header], "\r\n")) {
			header++
		}
		return lines, sectionSpan{header: header, end: at}, true, nil
	}
	return lines, span, false, nil
}

func samePathSegments(a, b []string) bool {
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

// headerPath splits a table name as written into its segments, unquoting
// each: `permissions.'my profile'.network` → permissions, my profile,
// network.
func headerPath(header string) []string {
	var out []string
	var cur strings.Builder
	rest := header
	for rest != "" {
		rest = strings.TrimLeft(rest, " \t")
		switch {
		case strings.HasPrefix(rest, "'"):
			end := strings.Index(rest[1:], "'")
			if end < 0 {
				return nil
			}
			cur.WriteString(rest[1 : 1+end])
			rest = rest[2+end:]
		case strings.HasPrefix(rest, `"`):
			var doc map[string]string
			end := -1
			for i := 1; i < len(rest); i++ {
				if rest[i] == '\\' {
					i++
					continue
				}
				if rest[i] == '"' {
					end = i
					break
				}
			}
			if end < 0 {
				return nil
			}
			if _, err := decodeKV(`k = `+rest[:end+1], &doc); err != nil {
				return nil
			}
			cur.WriteString(doc["k"])
			rest = rest[end+1:]
		default:
			n := strings.IndexAny(rest, ". \t")
			if n < 0 {
				n = len(rest)
			}
			cur.WriteString(rest[:n])
			rest = rest[n:]
		}
		rest = strings.TrimLeft(rest, " \t")
		if strings.HasPrefix(rest, ".") {
			out = append(out, cur.String())
			cur.Reset()
			rest = rest[1:]
			continue
		}
		if rest != "" {
			return nil
		}
	}
	out = append(out, cur.String())
	return out
}

// rewriteRootKey rewrites the root-level line setting key to
// `key = valueTOML`, marked with the original line so uninstall can put it
// back.
func (ed *codexEditor) rewriteRootKey(key, valueTOML string, value any) error {
	lines := strings.SplitAfter(ed.text, "\n")
	idx := firstHeaderStart(ed.text)
	rootEnd := len(lines)
	if idx >= 0 {
		rootEnd = strings.Count(ed.text[:idx], "\n")
	}
	i, err := keyLineIn(lines[:rootEnd], key)
	if err != nil {
		return err
	}
	return ed.rewriteLine(lines, i, key, valueTOML, value, nil)
}

// rewriteInSection rewrites the line setting key inside the table at path.
func (ed *codexEditor) rewriteInSection(path []string, key, valueTOML string, value any) error {
	lines, span, found, err := findSection(ed.text, path)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no [%s] table", strings.Join(path, "."))
	}
	body := lines[span.header+1 : span.end]
	i, err := keyLineIn(body, key)
	if err != nil {
		return err
	}
	return ed.rewriteLine(lines, span.header+1+i, key, valueTOML, value, path)
}

func (ed *codexEditor) rewriteLine(lines []string, i int, key, valueTOML string, value any, path []string) error {
	orig := strings.TrimRight(lines[i], "\r\n")
	if _, _, marked := parseCodexMark(orig); marked {
		return fmt.Errorf("the line setting %s already carries a jevlin mark", key)
	}
	m, err := ed.mark("was: " + orig)
	if err != nil {
		return err
	}
	lines[i] = key + " = " + valueTOML + "  " + m + lineEnding(lines[i])
	ed.text = strings.Join(lines, "")
	setTOMLPath(ed.expected, value, append(append([]string{}, path...), key)...)
	return nil
}

// commentOutRootKey turns the root-level line setting key into a comment,
// marked, so the profile replaces it and uninstall restores it.
func (ed *codexEditor) commentOutRootKey(key string) error {
	lines := strings.SplitAfter(ed.text, "\n")
	idx := firstHeaderStart(ed.text)
	rootEnd := len(lines)
	if idx >= 0 {
		rootEnd = strings.Count(ed.text[:idx], "\n")
	}
	i, err := keyLineIn(lines[:rootEnd], key)
	if err != nil {
		return err
	}
	orig := strings.TrimRight(lines[i], "\r\n")
	m, err := ed.mark(codexMarkReplaced)
	if err != nil {
		return err
	}
	lines[i] = "# " + orig + "  " + m + lineEnding(lines[i])
	ed.text = strings.Join(lines, "")
	deleteTOMLPath(ed.expected, key)
	return nil
}

const codexMarkReplaced = "replaced by the jevlin profile"

// lineEnding is the ending a line of the file had, so a line rewritten in a
// CRLF file stays CRLF.
func lineEnding(line string) string {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return "\r\n"
	case strings.HasSuffix(line, "\n"):
		return "\n"
	}
	return ""
}

// tomlEntry is one `key = value` line to add: the key and value as TOML
// text, and the value as it decodes.
type tomlEntry struct {
	keyTOML, valueTOML string
	key                string
	value              any
}

// addToSection appends marked entries to the table at path, creating the
// table at the end of the file — its header marked too — when there is
// none.
func (ed *codexEditor) addToSection(path []string, entries []tomlEntry) error {
	m, err := ed.mark("")
	if err != nil {
		return err
	}
	var add strings.Builder
	for _, e := range entries {
		add.WriteString(e.keyTOML + " = " + e.valueTOML + "  " + m + "\n")
		setTOMLPath(ed.expected, e.value, append(append([]string{}, path...), e.key)...)
	}
	lines, span, found, err := findSection(ed.text, path)
	if err != nil {
		return err
	}
	if !found {
		header := "[" + renderHeaderPath(path) + "]  " + m + "\n"
		ed.text = string(appendTables([]byte(ed.text), header+add.String()))
		return nil
	}
	// Before the blank run that ends the section, so the gap to the next
	// table stays where the participant left it.
	end := span.end
	for end > span.header+1 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if end > 0 && !strings.HasSuffix(lines[end-1], "\n") {
		lines[end-1] += "\n"
	}
	out := append([]string{}, lines[:end]...)
	out = append(out, add.String())
	out = append(out, lines[end:]...)
	ed.text = strings.Join(out, "")
	return nil
}

// renderHeaderPath spells a path as a header name, quoting what TOML's bare
// key grammar cannot carry.
func renderHeaderPath(path []string) string {
	parts := make([]string, len(path))
	for i, p := range path {
		if bareKeyRe.MatchString(p) {
			parts[i] = p
			continue
		}
		parts[i] = mustTOMLString(p)
	}
	return strings.Join(parts, ".")
}

var bareKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ── reading the marks back ──────────────────────────────────────────────

var codexMarkRe = regexp.MustCompile(`^(.*?)[ \t]*#[ \t]*` + regexp.QuoteMeta(codexMarkWord) + ` \(("(?:[^"\\]|\\.)*")\)(?:; (.*?))?[ \t]*$`)

// parseCodexMark reads a marked line: what precedes the mark, the config
// path it names, and the suffix.
func parseCodexMark(line string) (before, cfgPath string, marked bool) {
	m := codexMarkRe.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
	if m == nil {
		return "", "", false
	}
	var doc map[string]string
	if _, err := decodeKV("k = "+m[2], &doc); err != nil {
		return "", "", false
	}
	return m[1], doc["k"], true
}

func markSuffix(line string) string {
	m := codexMarkRe.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
	if m == nil {
		return ""
	}
	return m[3]
}

// codexMarkedLine is one line of ours found in a participant's table.
type codexMarkedLine struct {
	index  int
	path   []string // the table the line is in; nil at root
	header bool     // the line is a table header we added
	cfg    string
}

// codexMarkedLines finds every marked line in text and the table each sits
// in. The scan is the same header scan the rest of this file uses, and its
// result is held to the decoded document when the lines are acted on.
func codexMarkedLines(text string) []codexMarkedLine {
	var out []codexMarkedLine
	var path []string
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		if m := tomlHeaderLine.FindStringSubmatch(line); m != nil {
			path = headerPath(headerName(m))
			if _, cfg, marked := parseCodexMark(line); marked {
				out = append(out, codexMarkedLine{index: i, path: path, header: true, cfg: cfg})
			}
			continue
		}
		if _, cfg, marked := parseCodexMark(line); marked {
			out = append(out, codexMarkedLine{index: i, path: path, cfg: cfg})
		}
	}
	return out
}

// codexMarkRemoval is what taking this installation's marked lines out of
// the file comes to.
type codexMarkRemoval struct {
	next     []byte
	changed  bool
	restored []string // keys put back as they were
	removed  []string // entries deleted, as "[table] key"
	foreign  []string // config paths of marks that are another installation's
	why      string
}

// removeCodexMarks takes back exactly the lines marked with this
// installation's config path: an added entry or header is deleted, a
// rewritten or commented-out line is restored to the line the mark kept.
func removeCodexMarks(text string, entry binEntry) codexMarkRemoval {
	marks := codexMarkedLines(text)
	if len(marks) == 0 {
		return codexMarkRemoval{next: []byte(text)}
	}
	ed, err := newCodexEditor(text, entry.cfg)
	if err != nil {
		return codexMarkRemoval{next: []byte(text), why: err.Error()}
	}
	lines := strings.Split(text, "\n")
	out := codexMarkRemoval{}
	drop := map[int]bool{}
	for _, m := range marks {
		if entry.cfg == "" || !samePath(m.cfg, entry.cfg) {
			if !containsString(out.foreign, m.cfg) {
				out.foreign = append(out.foreign, m.cfg)
			}
			continue
		}
		line := strings.TrimRight(lines[m.index], "\r")
		cr := strings.TrimPrefix(lines[m.index], line) // "\r" in a CRLF file
		before, _, _ := parseCodexMark(line)
		suffix := markSuffix(line)
		switch {
		case m.header:
			drop[m.index] = true
			// appendTables put one blank line above the header it added;
			// it goes with the header.
			if m.index > 0 && strings.TrimSpace(lines[m.index-1]) == "" {
				drop[m.index-1] = true
			}
			deleteTOMLPath(ed.expected, m.path...)
		case strings.HasPrefix(suffix, "was: "):
			orig := strings.TrimPrefix(suffix, "was: ")
			lines[m.index] = orig + cr
			doc, ok := decodeTOMLDoc(orig)
			if !ok || len(doc) != 1 {
				return codexMarkRemoval{next: []byte(text), why: fmt.Sprintf("line %d carries a jevlin mark whose kept line does not read as one key; restore it by hand", m.index+1)}
			}
			for k, v := range doc {
				setTOMLPath(ed.expected, v, append(append([]string{}, m.path...), k)...)
				out.restored = append(out.restored, k)
			}
		case suffix == codexMarkReplaced:
			orig := strings.TrimPrefix(before, "# ")
			lines[m.index] = orig + cr
			doc, ok := decodeTOMLDoc(orig)
			if !ok || len(doc) != 1 {
				return codexMarkRemoval{next: []byte(text), why: fmt.Sprintf("line %d carries a jevlin mark whose commented-out line does not read as one key; restore it by hand", m.index+1)}
			}
			for k, v := range doc {
				setTOMLPath(ed.expected, v, append(append([]string{}, m.path...), k)...)
				out.restored = append(out.restored, k)
			}
		default:
			doc, ok := decodeTOMLDoc(before)
			if !ok || len(doc) != 1 {
				return codexMarkRemoval{next: []byte(text), why: fmt.Sprintf("line %d carries a jevlin mark but does not read as one key; remove it by hand", m.index+1)}
			}
			drop[m.index] = true
			for k := range doc {
				deleteTOMLPath(ed.expected, append(append([]string{}, m.path...), k)...)
				out.removed = append(out.removed, "["+strings.Join(m.path, ".")+"] "+k)
			}
		}
		out.changed = true
	}
	if !out.changed {
		return codexMarkRemoval{next: []byte(text), foreign: out.foreign}
	}
	var kept []string
	for i, l := range lines {
		if !drop[i] {
			kept = append(kept, l)
		}
	}
	ed.text = strings.Join(kept, "\n")
	if err := ed.verify(); err != nil {
		return codexMarkRemoval{next: []byte(text), why: err.Error() + "; remove the marked lines by hand", foreign: out.foreign}
	}
	out.next = []byte(ed.text)
	return out
}

// ── the ownership mark on a participant's own line ──────────────────────
//
// An entry added to a profile of the participant's (D3), a value of theirs
// rewritten (D4, C5) or a line of theirs commented out (C5) does not live
// between our markers, so each such line names its installation itself, in a
// trailing comment that carries this installation's config path — the same
// rule ownership_match.go applies to a hook command or a skill. Uninstall
// touches a marked line only when the path is this installation's.

const codexMarkWord = "jevlin agents install"

// codexMark renders the trailing comment for cfgPath. A path holding a
// control or line-separator character is refused (C7): a newline would end
// the comment and start a line of the participant's file that nobody wrote.
func codexMark(cfgPath, suffix string) (string, error) {
	for _, r := range cfgPath {
		if unicode.IsControl(r) || r == ' ' || r == ' ' || r == '\u0085' {
			return "", fmt.Errorf("the config path %q holds a control or line-separator character and cannot be written as a comment into Codex's config.toml; move the config to a plainer path", cfgPath)
		}
	}
	q, err := tomlString(cfgPath)
	if err != nil {
		return "", err
	}
	m := "# " + codexMarkWord + " (" + q + ")"
	if suffix != "" {
		m += "; " + suffix
	}
	return m, nil
}

// decodeKV decodes one small TOML text into v: the reader every mark and
// header segment goes through, so quoting is undone by the TOML decoder and
// never by hand.
func decodeKV(s string, v any) (toml.MetaData, error) { return toml.Decode(s, v) }
