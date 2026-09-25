package main

// Every path and Go identifier AGENTS.md names exists where it says.
//
// AGENTS.md sends a contributor to the file that owns a rule, the function
// that applies it and the test that proves it, by name. A file renamed or
// moved, or a function renamed, leaves that sentence pointing at nothing,
// and the reader who follows it is the one about to change the rule.
// Nothing else notices: the file is prose, the compiler never reads it, and
// the link test checks links, while AGENTS.md names its files and symbols
// in backticks. So a backticked name is checked the way a link is.
//
// Three kinds of backticked token are checked, and only those three. A
// token whose first segment is one of the repository's top-level entries
// (`cmd/jevlin/setup.go`, `pkg/auth`, `CHANGELOG.md`) is a path resolved
// against the module root. A bare Go file name (`connect.go`), which is how
// most of the document names a file, is looked for in the whole tree and
// must name exactly one file. A camelCase word (`searchTrace`) or a test
// name (`TestMain`) is a Go identifier, and must be declared somewhere in
// the module, tests included: a func or method, a type, a const or var
// (grouped or not), or a struct field. Everything else in backticks is
// something the document is talking about rather than a place in this tree
// — a file in the participant's state directory, an import path, a URL
// path, a prompt, a wire field like `as_url`, a status word like
// `degraded` — and matches none of the three, so no list of exceptions is
// needed. A lowercase word is left alone on purpose: in this document it is
// far more often a config key, a protocol code or plain English than a Go
// name, and checking it would need exactly the exception list this test
// exists to avoid. A path in another repository is written with that
// repository's name in front, and is skipped and counted.

import (
	"go/ast"
	"go/parser"
	"go/token"
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
	// A Go identifier as AGENTS.md writes one: camelCase, which starts
	// lowercase and has an uppercase letter in it, or a test name. No
	// underscore, so a wire field or a protocol code is never one.
	goIdentifierToken = regexp.MustCompile(`^(?:[a-z][a-z0-9]*[A-Z][A-Za-z0-9]*|Test[A-Z][A-Za-z0-9]*)$`)
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
	goIdentifier                    // declared somewhere in the module
)

// classifyAgentsToken says which kind of path, if any, a backticked token
// is, or that it is a Go identifier, given the names at the module root,
// and returns the token with any line suffix removed.
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
	case goIdentifierToken.MatchString(token):
		return goIdentifier, token
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
		case notAPath, goIdentifier:
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

// goDeclarations is every name declared in a .go file under root, tests
// included: funcs and methods, types, consts and vars wherever they are
// declared, grouped or not, and struct fields. The parser rather than a
// grep, because a grouped const (`lineageMaxAge`) or a struct field
// (`reserve`) is declared without its keyword in front of it.
func goDeclarations(t *testing.T, root string) map[string]bool {
	t.Helper()
	declared := map[string]bool{}
	fset := token.NewFileSet()
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
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				declared[n.Name.Name] = true
			case *ast.TypeSpec:
				declared[n.Name.Name] = true
			case *ast.ValueSpec:
				for _, name := range n.Names {
					declared[name.Name] = true
				}
			case *ast.StructType:
				for _, field := range n.Fields.List {
					for _, name := range field.Names {
						declared[name.Name] = true
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return declared
}

// Every kind of declaration the identifier class accepts is pinned on a
// fixture, because AGENTS.md need not cite each kind: it names no struct
// field in camelCase today, and one it names tomorrow must still be found.
func TestGoDeclarationsCollectsEveryKind(t *testing.T) {
	dir := t.TempDir()
	src := `package p

func topFunc() {}

type recvType struct {
	fieldName  int
	a, bOther  string
	embeddedNoName
}

func (recvType) methodName() {}

const (
	groupedConst = 1
	groupedIota  = iota
)

var (
	groupedVar int
)

const plainConst = 1

var plainVar int

func inner() {
	const localConst = 1
	var localVar int
	shortDecl := localVar
	_ = shortDecl
}
`
	if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	declared := goDeclarations(t, dir)
	for _, name := range []string{"topFunc", "recvType", "fieldName", "a", "bOther", "methodName",
		"groupedConst", "groupedIota", "groupedVar", "plainConst", "plainVar", "localConst", "localVar"} {
		if !declared[name] {
			t.Errorf("%s is declared in the fixture but was not collected", name)
		}
	}
	// A short variable declaration is an assignment, not one of the kinds
	// named; neither it nor a name only used is collected.
	for _, name := range []string{"shortDecl", "embeddedNoName"} {
		if declared[name] {
			t.Errorf("%s was collected, but it is not declared as a func, type, const, var or named field", name)
		}
	}
}

func TestAgentsMDIdentifiersAreDeclared(t *testing.T) {
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "AGENTS.md")) // #nosec G304 -- a fixed document under this module's own root
	if err != nil {
		t.Fatal(err)
	}
	declared := goDeclarations(t, root)

	checked := 0
	for _, span := range agentsCodeSpans(t, body) {
		class, name := classifyAgentsToken(span.target, nil)
		if class != goIdentifier {
			continue
		}
		checked++
		if !declared[name] {
			t.Errorf("AGENTS.md:%d: `%s`: no func, method, type, const, var or struct field of that name anywhere in the module", span.line, name)
		}
	}

	// A scanner that finds nothing passes everything. AGENTS.md names 110
	// Go identifiers as this test was written, so a count below three
	// quarters of that means the scan broke, not that the document stopped
	// naming code.
	t.Logf("checked %d identifiers", checked)
	if checked < 82 {
		t.Fatalf("checked %d identifiers: the scan found too few to mean anything", checked)
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
		"searchTrace":                             goIdentifier,
		"hermesRunIsRenderedExactly":              goIdentifier,
		"TestMain":                                goIdentifier,
		"TestNoChainImportsAnywhere":              goIdentifier,
		"as_url":                                  notAPath,
		"decision_unreadable":                     notAPath,
		"degraded":                                notAPath,
		"y":                                       notAPath,
		"Testing":                                 notAPath,
		"Prepared.DiscardAfterInstall":            notAPath,
		"proceeded()":                             notAPath,
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
