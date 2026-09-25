package main

// Every relative link in the human documents resolves, anchor included.
//
// The documentation is four files that link into each other by heading:
// the README points at sections of the guide, the guide at the reference,
// every host row at its section of the agents page. A heading renamed in
// one file breaks a link in another, silently: GitHub renders a dead
// anchor as a link to the top of the page, and nobody notices until a
// participant lands in the wrong place. So a link is checked the way code
// is: the file must exist, and an anchor must be a heading that file
// actually has, slugged the way GitHub slugs it.

import (
	"bufio"
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

// linkedDocs is the set of documents whose links are checked, relative to
// the module root. A glob for docs/, so a page added there is covered
// without anyone remembering to add it here.
func linkedDocs(t *testing.T, root string) []string {
	t.Helper()
	docs, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"README.md", "CONTRIBUTING.md", "SECURITY.md"} {
		docs = append(docs, filepath.Join(root, name))
	}
	return docs
}

var (
	// An inline link or image: [text](target) or [text](target "title").
	inlineLink = regexp.MustCompile(`!?\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	// A reference definition: [label]: target
	refDefinition = regexp.MustCompile(`^\s{0,3}\[[^\]]+\]:\s*(\S+)`)
	// An inline code span, removed before links are looked for, so a link
	// written as an example inside backticks is not taken for a real one.
	codeSpan = regexp.MustCompile("`[^`]*`")
	// An ATX heading. Setext headings are not used in these documents.
	atxHeading = regexp.MustCompile(`^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$`)
	// A link inside heading text keeps only its text, as GitHub renders it.
	headingLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

// githubSlug is the anchor GitHub gives a heading's text: lowercased,
// every character that is not a letter, mark, number, underscore, hyphen
// or space removed, and each space turned into a hyphen. Runs of hyphens
// are not collapsed, which is why "The `--stdin` request" is
// "the---stdin-request".
func githubSlug(heading string) string {
	text := headingLink.ReplaceAllString(heading, "$1")
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// markdownLines yields each line outside fenced code blocks, with its
// 1-based number. A fence is ``` or ~~~, and closes on the same marker.
func markdownLines(body []byte, visit func(n int, line string)) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	fence := ""
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
			continue
		}
		visit(n, line)
	}
}

// headingAnchors is every anchor a Markdown file defines, duplicates
// numbered as GitHub numbers them: the second "setup" is "setup-1".
func headingAnchors(body []byte) map[string]bool {
	anchors := map[string]bool{}
	seen := map[string]int{}
	markdownLines(body, func(_ int, line string) {
		m := atxHeading.FindStringSubmatch(line)
		if m == nil {
			return
		}
		slug := githubSlug(m[2])
		if n := seen[slug]; n > 0 {
			anchors[fmt.Sprintf("%s-%d", slug, n)] = true
		} else {
			anchors[slug] = true
		}
		seen[slug]++
	})
	return anchors
}

type docLink struct {
	line   int
	target string
}

// relativeLinks is every link in a document that points into the
// repository: not a URL with a scheme, not a mail address.
func relativeLinks(body []byte) []docLink {
	var out []docLink
	markdownLines(body, func(n int, line string) {
		line = codeSpan.ReplaceAllString(line, "")
		var targets []string
		for _, m := range inlineLink.FindAllStringSubmatch(line, -1) {
			targets = append(targets, m[1])
		}
		if m := refDefinition.FindStringSubmatch(line); m != nil {
			targets = append(targets, m[1])
		}
		for _, target := range targets {
			target = strings.Trim(target, "<>")
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			out = append(out, docLink{line: n, target: target})
		}
	})
	return out
}

func TestDocumentLinksResolve(t *testing.T) {
	root := moduleRoot(t)
	anchorsOf := map[string]map[string]bool{}
	anchors := func(path string) (map[string]bool, error) {
		if a, ok := anchorsOf[path]; ok {
			return a, nil
		}
		body, err := os.ReadFile(path) // #nosec G304 -- a Markdown file under this module's own root, named by a link in one
		if err != nil {
			return nil, err
		}
		anchorsOf[path] = headingAnchors(body)
		return anchorsOf[path], nil
	}

	var links, withAnchor int
	for _, doc := range linkedDocs(t, root) {
		body, err := os.ReadFile(doc) // #nosec G304 -- a fixed document under this module's own root
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		rel, _ := filepath.Rel(root, doc)
		for _, l := range relativeLinks(body) {
			links++
			pathPart, anchor, _ := strings.Cut(l.target, "#")
			target := doc
			if pathPart != "" {
				unescaped, err := url.PathUnescape(pathPart)
				if err != nil {
					t.Errorf("%s:%d: link %q: %v", rel, l.line, l.target, err)
					continue
				}
				if strings.HasPrefix(unescaped, "/") {
					target = filepath.Join(root, filepath.FromSlash(unescaped))
				} else {
					target = filepath.Join(filepath.Dir(doc), filepath.FromSlash(unescaped))
				}
				if _, err := os.Stat(target); err != nil {
					t.Errorf("%s:%d: link %q: no such file %s", rel, l.line, l.target, target)
					continue
				}
			}
			if anchor == "" {
				continue
			}
			withAnchor++
			if !strings.EqualFold(filepath.Ext(target), ".md") {
				t.Errorf("%s:%d: link %q: an anchor into a file that is not Markdown", rel, l.line, l.target)
				continue
			}
			a, err := anchors(target)
			if err != nil {
				t.Errorf("%s:%d: link %q: %v", rel, l.line, l.target, err)
				continue
			}
			if !a[anchor] {
				t.Errorf("%s:%d: link %q: %s has no heading with anchor #%s", rel, l.line, l.target, filepath.Base(target), anchor)
			}
		}
	}

	// A scanner that finds nothing passes everything. The README alone
	// links into the guide and the agents page by anchor more than a dozen
	// times, so a count below these floors means the scan broke, not that
	// the documents stopped linking.
	t.Logf("checked %d relative links, %d with an anchor", links, withAnchor)
	if links < 20 || withAnchor < 10 {
		t.Fatalf("checked %d relative links, %d with an anchor: the link scan found too few to mean anything", links, withAnchor)
	}
}

// The slug rule is GitHub's, and every anchor check above rests on it, so
// it is pinned against headings of these documents and the anchors
// GitHub's rendering of the pages gives them, rather than against values
// derived from this function.
func TestGithubSlugMatchesGitHubsAnchors(t *testing.T) {
	for heading, want := range map[string]string{
		"The `--stdin` request":         "the---stdin-request",
		"Removing it and coming back":   "removing-it-and-coming-back",
		"Making it the default, or not": "making-it-the-default-or-not",
		"When doctor is unsure":         "when-doctor-is-unsure",
		"Claude Code":                   "claude-code",
		"Where rewards land":            "where-rewards-land",
	} {
		if got := githubSlug(heading); got != want {
			t.Errorf("githubSlug(%q) = %q, want %q", heading, got, want)
		}
	}

	anchors := headingAnchors([]byte("# Setup\n\n```\n# not a heading\n```\n\n## Setup\n### Setup\n"))
	for _, want := range []string{"setup", "setup-1", "setup-2"} {
		if !anchors[want] {
			t.Errorf("headingAnchors: missing %q in %v", want, anchors)
		}
	}
	if anchors["not-a-heading"] {
		t.Errorf("headingAnchors: a line inside a code fence was taken for a heading")
	}
}
