package main

// Lines of the participant's own in Codex's config.toml that install
// changes, and their undoing.
//
// Everything this client adds to config.toml lives between its two markers;
// nothing is ever written inside a table of the participant's. Some lines of
// theirs would fight our profile, and those are changed in place, only after
// the participant typed yes to a question that showed them:
// `default_permissions` is rewritten to name our profile (which then extends
// the one it named), a bare `sandbox_mode = "workspace-write"` is commented
// out, and `network_proxy = false` is rewritten to true when our profile has
// a network of its own. Each such line carries a trailing comment naming the
// installation that wrote it, by its config path, and keeps the line it
// replaced; uninstall puts that line back.

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
)

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
	original string
	text     string
	expected tomlDoc
	cfgPath  string
}

func newCodexEditor(text, cfgPath string) (*codexEditor, error) {
	doc, ok := decodeTOMLDoc(text)
	if !ok {
		return nil, errors.New("the file does not read as TOML")
	}
	return &codexEditor{original: text, text: text, expected: doc, cfgPath: cfgPath}, nil
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
		was, ok := ourOriginal(orig, ed.cfgPath, key)
		if !ok {
			return fmt.Errorf("the line setting %s carries a jevlin mark that is not this installation's, or not one this client writes on a changed line", key)
		}
		// Codex rewrote the value of a line we had marked and kept our
		// mark (its app server does, switching default_permissions back
		// to ":workspace"): the line is ours to set again, and what it was
		// before our first change is still what uninstall puts back.
		orig = was
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

// ourOriginal is the line a marked line replaced, when the mark is this
// installation's and records one that sets key, and only key: the line
// before our first change. A "was:" naming another key is not one this
// client writes, and reading it would promise back a value uninstall
// cannot put back.
func ourOriginal(line, cfgPath, key string) (string, bool) {
	_, cfg, marked := parseCodexMark(line)
	was, isWas := strings.CutPrefix(markSuffix(line), "was: ")
	if !marked || !isWas || !sameConfigFile(cfg, cfgPath) {
		return "", false
	}
	if doc, ok := decodeTOMLDoc(was); !ok || len(doc) != 1 || doc[key] == nil {
		return "", false
	}
	return was, true
}

// rootKeyRestores is the value uninstall puts back for the root-level key,
// once install has rewritten its line: the value the line had before our
// first change when it already carries our mark, else its value now.
func rootKeyRestores(text, cfgPath, key, now string) string {
	lines := strings.SplitAfter(text, "\n")
	rootEnd := len(lines)
	if idx := firstHeaderStart(text); idx >= 0 {
		rootEnd = strings.Count(text[:idx], "\n")
	}
	i, err := keyLineIn(lines[:rootEnd], key)
	if err != nil {
		return now
	}
	was, ok := ourOriginal(strings.TrimRight(lines[i], "\r\n"), cfgPath, key)
	if !ok {
		return now
	}
	if doc, ok := decodeTOMLDoc(was); ok {
		if v, ok := doc[key].(string); ok {
			return v
		}
	}
	return now
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

// ── reading the marks back ──────────────────────────────────────────────

var codexMarkRe = regexp.MustCompile(`^(.*?)[ \t]*#[ \t]*` + regexp.QuoteMeta(codexMarkWord) + ` \(("(?:[^"\\]|\\.)*")\)(?:; (.*))?$`)

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
	index int
	path  []string // the table the line is in; nil at root
	cfg   string
}

// codexMarkedLines finds every marked line of the file's own structure —
// never one inside a multi-line string — and the table each sits in.
func codexMarkedLines(text string) []codexMarkedLine {
	var out []codexMarkedLine
	var path []string
	lineNo := map[int]int{} // byte offset of a line's start → its index in strings.Split(text, "\n")
	n := 0
	lineNo[0] = 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			n++
			lineNo[i+1] = n
		}
	}
	for _, l := range linesOutsideTOMLStrings(text) {
		line := strings.TrimRight(text[l.start:l.end], "\r")
		if m := tomlHeaderLine.FindStringSubmatch(line); m != nil {
			path = headerPath(headerName(m))
			continue
		}
		if _, cfg, marked := parseCodexMark(line); marked {
			out = append(out, codexMarkedLine{index: lineNo[l.start], path: path, cfg: cfg})
		}
	}
	return out
}

// codexPutBackAdvice is what a participant told to remove our block by
// hand must also do: put back each line of theirs jevlin changed, which
// the block's removal alone does not, and without which Codex refuses the
// file (a default_permissions naming a profile that is gone). Empty when
// no such line is marked.
func codexPutBackAdvice(text string) string {
	lines := strings.Split(text, "\n")
	var parts []string
	for _, m := range codexMarkedLines(text) {
		line := strings.TrimRight(lines[m.index], "\r")
		before, _, _ := parseCodexMark(line)
		var orig string
		switch suffix := markSuffix(line); {
		case strings.HasPrefix(suffix, "was: "):
			orig = strings.TrimPrefix(suffix, "was: ")
		case suffix == codexMarkReplaced:
			orig = strings.TrimPrefix(before, "# ")
		default:
			continue
		}
		parts = append(parts, fmt.Sprintf("line %d, %s, back to %s", m.index+1, strings.TrimSpace(before), orig))
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return "; then put back the line of yours jevlin changed: " + parts[0]
	}
	return "; then put back the lines of yours jevlin changed: " + strings.Join(parts, "; ")
}

// codexOrphanDefault finds, in a file with no jevlin block, a root-level
// default_permissions = "jevlin" line carrying jevlin's mark while no table
// defines that profile: what removing the block by hand without putting
// the line back leaves, and a file Codex refuses to load. It returns the
// sentence that says so and names the command that repairs it — the
// uninstall of the installation the mark names, which puts back the line
// the mark keeps — or "" when there is no such line.
func codexOrphanDefault(text, where string) string {
	doc, ok := decodeTOMLDoc(text)
	if !ok || doc["default_permissions"] != codexProfileName {
		return ""
	}
	if _, defined := lookupTOMLPath(doc, "permissions", codexProfileName); defined {
		return ""
	}
	lines := strings.Split(text, "\n")
	for _, m := range codexMarkedLines(text) {
		if m.path != nil {
			continue
		}
		line := strings.TrimRight(lines[m.index], "\r")
		before, _, _ := parseCodexMark(line)
		if now, ok := decodeTOMLDoc(before); !ok || now["default_permissions"] != codexProfileName {
			continue
		}
		was, isWas := strings.CutPrefix(markSuffix(line), "was: ")
		if !isWas {
			continue
		}
		return fmt.Sprintf("line %d of %s, %s, carries jevlin's mark, but the jevlin profile it names is gone, so Codex cannot load this file; `jevlin agents uninstall -client codex -config %s` puts back %s", m.index+1, where, strings.TrimSpace(before), displayNamedPath(m.cfg), was)
	}
	return ""
}

// codexMarkRemoval is what putting back the participant's own lines comes
// to.
type codexMarkRemoval struct {
	next     []byte
	changed  bool
	restored []string // the lines put back as they were, for the plan
	kept     []string // keys left, because their value changed after install
	foreign  []string // config paths of marks that are another installation's
	why      string
}

// codexMarkWrote is the value install writes over a marked line's key: our
// profile's name into default_permissions, true into a network_proxy switch.
func codexMarkWrote(key string) any {
	if key == "default_permissions" {
		return codexProfileName
	}
	return true
}

// restoreCodexMarks puts back the participant's own lines that install
// changed. withRegion is true when our region goes in the same edit: the
// region and the lines it implies are one unit (a default_permissions
// naming our profile is meaningless without it, and a network_proxy turned
// back off beside a region left in place would open its network), so every
// marked line is put back with it, whichever config path the mark spells.
// Without a region, a mark is this installation's only when it names this
// installation's config file — the same file, through any link.
//
// A line whose value is no longer what install wrote is the participant's
// newer choice and stays: putting the old line back over it could widen a
// sandbox they had since narrowed.
func restoreCodexMarks(text string, withRegion bool, entry binEntry) codexMarkRemoval {
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
	for _, m := range marks {
		if !withRegion && !sameConfigFile(m.cfg, entry.cfg) {
			if !containsString(out.foreign, m.cfg) {
				out.foreign = append(out.foreign, m.cfg)
			}
			continue
		}
		line := strings.TrimRight(lines[m.index], "\r")
		cr := strings.TrimPrefix(lines[m.index], line) // "\r" in a CRLF file
		before, _, _ := parseCodexMark(line)
		suffix := markSuffix(line)
		var orig string
		switch {
		case strings.HasPrefix(suffix, "was: "):
			orig = strings.TrimPrefix(suffix, "was: ")
			now, ok := decodeTOMLDoc(before)
			kept, okKept := decodeTOMLDoc(orig)
			if !ok || !okKept || len(now) != 1 || len(kept) != 1 {
				return codexMarkRemoval{next: []byte(text), why: fmt.Sprintf("line %d carries a jevlin mark that does not read as one key; put it back by hand", m.index+1)}
			}
			key := sortedKeys(kept)[0]
			if _, same := now[key]; !same {
				// The kept line sets another key than the line carrying the
				// mark: not a mark this client writes, and neither line is
				// one to put back or take the mark off by its name.
				return codexMarkRemoval{next: []byte(text), why: fmt.Sprintf("line %d carries a jevlin mark whose kept line sets another key; put it back by hand", m.index+1)}
			}
			if v := now[key]; !tomlValueEqual(v, codexMarkWrote(key)) {
				// The participant's newer choice stays, without our mark:
				// the line is theirs now, and a mark left on it would make
				// the next switch refuse it as already ours.
				out.kept = append(out.kept, key)
				lines[m.index] = before + cr
				out.changed = true
				continue
			}
		case suffix == codexMarkReplaced:
			orig = strings.TrimPrefix(before, "# ")
		default:
			return codexMarkRemoval{next: []byte(text), why: fmt.Sprintf("line %d carries a jevlin mark but keeps no line of yours to put back; put it back by hand", m.index+1)}
		}
		doc, ok := decodeTOMLDoc(orig)
		if !ok || len(doc) != 1 {
			return codexMarkRemoval{next: []byte(text), why: fmt.Sprintf("line %d carries a jevlin mark whose kept line does not read as one key; put it back by hand", m.index+1)}
		}
		lines[m.index] = orig + cr
		for k, v := range doc {
			setTOMLPath(ed.expected, v, append(append([]string{}, m.path...), k)...)
		}
		// The line itself, so the plan names the value it puts back: after
		// a rewrite Codex made under our mark, that is the value before our
		// first change, not the one in the file now.
		out.restored = append(out.restored, strings.TrimSpace(orig))
		out.changed = true
	}
	if !out.changed {
		return codexMarkRemoval{next: []byte(text), foreign: out.foreign, kept: out.kept}
	}
	ed.text = strings.Join(lines, "\n")
	if err := ed.verify(); err != nil {
		return codexMarkRemoval{next: []byte(text), why: err.Error() + "; put the marked lines back by hand", foreign: out.foreign}
	}
	out.next = []byte(ed.text)
	return out
}

func tomlValueEqual(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

// sameConfigFile: do a and b name the same config file, compared as paths
// and then through any symbolic link? An empty name is no file.
func sameConfigFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if samePath(a, b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && samePath(ra, rb)
}

// ── the ownership mark on a participant's own line ──────────────────────
//
// An entry added to a profile of the participant's, a value of theirs
// rewritten or a line of theirs commented out does not live
// between our markers, so each such line names its installation itself, in a
// trailing comment that carries this installation's config path — the same
// rule ownership_match.go applies to a hook command or a skill. Uninstall
// touches a marked line only when the path is this installation's.

const codexMarkWord = "jevlin agents install"

// codexMark renders the trailing comment for cfgPath. A path holding a
// control or line-separator character is refused: a newline would end
// the comment and start a line of the participant's file that nobody wrote.
func codexMark(cfgPath, suffix string) (string, error) {
	for _, r := range cfgPath {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == '\u0085' {
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
