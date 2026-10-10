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
			return codexMarkRemoval{next: []byte(text), why: fmt.Sprintf("line %d carries a jevlin mark but keeps no line of yours to put back; remove it by hand", m.index+1)}
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
