package main

// Every path AGENTS.md names exists where it says.
//
// AGENTS.md sends a contributor to the file that owns a rule and the test
// that proves it, by name. A file renamed or moved leaves that sentence
// pointing at nothing, and the reader who follows it is the one about to
// change the rule. Nothing else notices: the file is prose, the compiler
// never reads it, and the link test checks links, while AGENTS.md names its
// files in backticks. So a backticked path is checked the way a link is.
//
// Two kinds of backticked token are paths in this repository, and only
// those two are checked. A token whose first segment is one of the
// repository's top-level entries (`cmd/jevlin/setup.go`, `pkg/auth`,
// `CHANGELOG.md`) is resolved against the module root. A bare Go file name
// (`connect.go`), which is how most of the document names a file, is looked
// for in the whole tree and must name exactly one file. Everything else in
// backticks is something the document is talking about rather than a place
// in this tree — a file in the participant's state directory, an import
// path, a URL path, a prompt — and matches neither kind, so no list of
// exceptions is needed. A path in another repository is written with that
// repository's name in front, and is skipped and counted.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// otherRepositories are the repositories AGENTS.md names paths in. A token
// that begins with one of these followed by a slash is a path over there,
// which this test cannot check.
var otherRepositories = map[string]bool{
	"tokendrop-auth-server-design": true,
	"search-router":                true,
	"twilight-project":             true,
}

var (
	// An inline code span. Unlike a link it can run across a line break,
	// which reads as a space.
	agentsCodeSpan = regexp.MustCompile("`([^`]+)`")
	// A line break inside a code span, with the next line's indentation.
	spanLineBreak = regexp.MustCompile(`\n[ \t]*`)
	// A `file.go:NNN` suffix names a line; only the file is checked.
	lineSuffix = regexp.MustCompile(`:\d+$`)
)

// fileExtensions are the endings that say a token names a file rather than
// a directory, so it must be a regular file.
var fileExtensions = []string{".go", ".md", ".yml", ".yaml", ".json", ".toml", ".js", ".ts", ".py", ".golden"}

type pathClass int

const (
	notAPath       pathClass = iota // not a place in this tree; not checked
	otherRepoPath                   // a path in another repository; skipped and counted
	rootPath                        // resolved against the module root
	bareGoFileName                  // looked for in the whole tree
)

// classifyAgentsToken says which kind of path a backticked token is, given
// the names at the module root, and returns the token with any line suffix
// removed.
func classifyAgentsToken(token string, rootEntries map[string]bool) (pathClass, string) {
	token = lineSuffix.ReplaceAllString(token, "")
	first, _, hasSlash := strings.Cut(token, "/")
	switch {
	case hasSlash && otherRepositories[first]:
		return otherRepoPath, token
	case rootEntries[first]:
		return rootPath, token
	case !hasSlash && !strings.ContainsAny(token, " \t") &&
		(strings.HasSuffix(token, ".go") || strings.HasSuffix(token, ".golden")):
		return bareGoFileName, token
	}
	return notAPath, token
}

// agentsCodeSpans is every inline code span outside a fenced block, with
// the line it starts on.
func agentsCodeSpans(t *testing.T, body []byte) []docLink {
	t.Helper()
	var text strings.Builder
	var starts, numbers []int
	markdownLines(body, func(n int, line string) {
		starts = append(starts, text.Len())
		numbers = append(numbers, n)
		text.WriteString(line)
		text.WriteByte('\n')
	})
	s := text.String()
	var out []docLink
	for _, m := range agentsCodeSpan.FindAllStringSubmatchIndex(s, -1) {
		line := numbers[sort.SearchInts(starts, m[0]+1)-1]
		raw := s[m[2]:m[3]]
		// A code span ends at a blank line. One that seems to cross it is
		// a backtick left unclosed, and every span after it would be read
		// inside out.
		if strings.Contains(raw, "\n\n") {
			t.Fatalf("AGENTS.md:%d: a code span runs across a blank line: an unbalanced backtick", line)
		}
		out = append(out, docLink{line: line, target: spanLineBreak.ReplaceAllString(raw, " ")})
	}
	return out
}

// goFilesByName indexes every .go and .golden file under root by base name.
func goFilesByName(t *testing.T, root string) map[string][]string {
	t.Helper()
	index := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if name := d.Name(); strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".golden") {
			rel, _ := filepath.Rel(root, path)
			index[name] = append(index[name], filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return index
}

func TestAgentsMDPathsExist(t *testing.T) {
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "AGENTS.md")) // #nosec G304 -- a fixed document under this module's own root
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	rootEntries := map[string]bool{}
	for _, e := range entries {
		if e.Name() != ".git" {
			rootEntries[e.Name()] = true
		}
	}
	byName := goFilesByName(t, root)

	var checked, skipped int
	for _, span := range agentsCodeSpans(t, body) {
		class, token := classifyAgentsToken(span.target, rootEntries)
		switch class {
		case notAPath:
			continue
		case otherRepoPath:
			skipped++
			continue
		case bareGoFileName:
			checked++
			switch found := byName[token]; len(found) {
			case 0:
				t.Errorf("AGENTS.md:%d: `%s`: no such file anywhere in the module", span.line, token)
			case 1:
			default:
				t.Errorf("AGENTS.md:%d: `%s`: names %d files (%s); say which directory", span.line, token, len(found), strings.Join(found, ", "))
			}
			continue
		}

		checked++
		target := filepath.Join(root, filepath.FromSlash(token))
		if strings.Contains(token, "*") {
			matches, err := filepath.Glob(target)
			if err != nil || len(matches) == 0 {
				t.Errorf("AGENTS.md:%d: `%s`: the glob matches nothing", span.line, token)
			}
			continue
		}
		info, err := os.Stat(target)
		switch {
		case err != nil:
			t.Errorf("AGENTS.md:%d: `%s`: no such file or directory %s", span.line, token, target)
		case strings.HasSuffix(token, "/") && !info.IsDir():
			t.Errorf("AGENTS.md:%d: `%s`: not a directory", span.line, token)
		case namesAFile(token) && !info.Mode().IsRegular():
			t.Errorf("AGENTS.md:%d: `%s`: not a file", span.line, token)
		}
	}

	// A scanner that finds nothing passes everything. AGENTS.md names
	// 162 paths in this repository as this test was written, so a count
	// below three quarters of that means the scan broke, not that the
	// document stopped naming files.
	t.Logf("checked %d paths, skipped %d in other repositories", checked, skipped)
	if checked < 121 {
		t.Fatalf("checked %d paths: the scan found too few to mean anything", checked)
	}
}

func namesAFile(token string) bool {
	for _, ext := range fileExtensions {
		if strings.HasSuffix(token, ext) {
			return true
		}
	}
	return false
}

// The classification is the whole argument that no exception list is
// needed, so it is pinned against the kinds of token AGENTS.md actually
// holds.
func TestAgentsMDTokensAreClassified(t *testing.T) {
	rootEntries := map[string]bool{"cmd": true, "pkg": true, "testdata": true, "CHANGELOG.md": true}
	for token, want := range map[string]pathClass{
		"cmd/jevlin/setup.go":                     rootPath,
		"cmd/jevlin/setup.go:120":                 rootPath,
		"pkg/auth":                                rootPath,
		"testdata/hermes/*.yaml":                  rootPath,
		"CHANGELOG.md":                            rootPath,
		"connect.go":                              bareGoFileName,
		"claude.install.golden":                   bareGoFileName,
		"tokendrop-auth-server-design/docs/spec/": otherRepoPath,
		"search-router/README.md":                 otherRepoPath,
		"tokendrop-auth-server-design":            notAPath,
		"credentials.json":                        notAPath,
		"wallet/pending_tx.json":                  notAPath,
		"state/":                                  notAPath,
		"~/.claude/settings.json":                 notAPath,
		"net/http":                                notAPath,
		"GET /v1/agents/me":                       notAPath,
		"/tx":                                     notAPath,
		"Enable mining rewards? [y/N]":            notAPath,
		"jevlinai/jevlin-go":                      notAPath,
	} {
		if got, _ := classifyAgentsToken(token, rootEntries); got != want {
			t.Errorf("classifyAgentsToken(%q) = %d, want %d", token, got, want)
		}
	}
	if _, stripped := classifyAgentsToken("cmd/jevlin/setup.go:120", rootEntries); stripped != "cmd/jevlin/setup.go" {
		t.Errorf("a line suffix was not removed: %q", stripped)
	}

	spans := agentsCodeSpans(t, []byte("see `a/b.go` and `c/\n  d.md`\n\n```\n`e.go`\n```\n`f.go`\n"))
	var got []string
	for _, s := range spans {
		got = append(got, s.target)
	}
	if want := []string{"a/b.go", "c/ d.md", "f.go"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("agentsCodeSpans = %q, want %q", got, want)
	}
	if spans[2].line != 7 {
		t.Errorf("agentsCodeSpans: `f.go` on line %d, want 7", spans[2].line)
	}
}
