package main

// The folder a Claude Code request is written to when it cannot ride on the
// command line.
//
// The one-line call carries the request as a single-quoted word, and Claude
// Code refuses a command in which a quote ends that word early or a `$name`
// would expand. An agent told to write those two characters as JSON escapes
// decoded them back before sending, so a request holding either goes through
// a file instead: the agent writes it with the Write tool and the call reads
// it from standard input.
//
//	'<bin>' search -config '<cfg>' --stdin < '<dir>/<name>.json'
//
// The folder is `requests` beside the config, so it belongs to one
// installation, and nothing here creates it: the Write tool makes a missing
// parent (seen on Claude Code 2.1.312), and the home it sits in is already
// owner-only. The allow rules below are what let that Write and that read
// happen without a question, and the hook recognizes the call only when the
// file is a direct child of the folder (requestFileBody).
//
// Nothing prunes the folder. The request text is also in the transcript
// Claude Code keeps, so a file here adds no exposure; a prune would put a
// directory scan and a delete on the hook that runs before every search.

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

const requestsDirName = "requests"

// requestsDirFor is the folder for the installation whose config is cfg, or
// "" when there is no config to anchor it to. It is built from the text of
// cfg, not with the running OS's path functions, so a Windows config renders
// the same folder on any machine (the host-strings goldens render all three
// OSes everywhere); on the OS the config is for, it is filepath.Join of the
// config's directory.
func requestsDirFor(cfg string) string {
	cut := strings.LastIndexAny(cfg, `/\`)
	if cfg == "" || cut < 0 {
		return ""
	}
	return cfg[:cut] + sepOf(cfg) + requestsDirName
}

// sepOf is the separator a path is spelled with.
func sepOf(p string) string {
	if strings.Contains(p, `\`) {
		return `\`
	}
	return "/"
}

// requestFilePath is a file named name in the folder dir.
func requestFilePath(dir, name string) string { return dir + sepOf(dir) + name }

// claudeRulePath spells an absolute path the way a Claude Code permission
// rule names a file outside the project: a second leading slash, and on
// Windows the drive as a first path element (`C:\x` is `//c/x`).
func claudeRulePath(dir string) string {
	p := strings.ReplaceAll(dir, `\`, "/")
	if len(p) >= 2 && p[1] == ':' {
		p = "/" + strings.ToLower(p[:1]) + p[2:]
	}
	return "/" + p
}

// claudeRequestRules are the two rules the file form needs. Claude Code's
// Write is governed by Edit rules, not by Write rules (a Write rule did not
// allow a write; an Edit rule did), and the call's `<` file is checked
// against Read rules, so the folder needs one of each. Both name the folder
// and nothing above it.
func claudeRequestRules(entry binEntry) []string {
	dir := requestsDirFor(entry.cfg)
	if dir == "" {
		return nil
	}
	p := claudeRulePath(dir) + "/**"
	return []string{"Edit(" + p + ")", "Read(" + p + ")"}
}

// claudeSkillRule lets Claude Code read the skill's bundled reference.md
// without asking: a Read rule for the skill's own folder, nothing above it.
func claudeSkillRule(skillFile string) string {
	return "Read(" + claudeRulePath(filepath.Dir(skillFile)) + "/**)"
}

// skillRuleIsOurs: is this the Read rule for a skills/jevlin folder? A host
// has one such folder, so the rule is attributed by that name alone.
func skillRuleIsOurs(r string) bool {
	inner, ok := strings.CutPrefix(r, "Read(")
	if !ok {
		return false
	}
	inner, ok = strings.CutSuffix(inner, ")")
	return ok && strings.HasSuffix(strings.ReplaceAll(inner, `\`, "/"), "/skills/"+agentsName+"/**")
}

// requestRuleIsOurs: is this an Edit or Read rule for the requests folder
// of the installation ref names? The folder is the installation's, so a rule
// for another config's folder is left.
func requestRuleIsOurs(r string, ref installationRef) bool {
	dir := requestsDirFor(ref.cfg)
	// A folder rule names no binary, so it is attributed by the config
	// alone; the uninstall probe has no binary on purpose, and a rule must
	// not be taken for the probe's (TestNoHookFileCanEnterTheAttributionPlan).
	if slices.Contains(ref.bins, uninstallProbeCommand) {
		return false
	}
	if skillRuleIsOurs(r) {
		return true
	}
	if dir == "" {
		return false
	}
	want := claudeRulePath(dir) + "/**"
	for _, tool := range []string{"Edit(", "Read("} {
		if inner, ok := strings.CutPrefix(r, tool); ok {
			inner, ok = strings.CutSuffix(inner, ")")
			return ok && sameRulePath(inner, want)
		}
	}
	return false
}

func sameRulePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// requestFileBody reads the request a file-form call names, or reports that
// the call is not one the hook may recognize: the file must be a direct
// child of the installation's requests folder, spelled cleanly, and hold one
// version-1 object, the same rule a here-string body is held to.
func requestFileBody(file, cfg string, readFile func(string) ([]byte, error)) (string, bool) {
	dir := requestsDirFor(cfg)
	if dir == "" || readFile == nil || !filepath.IsAbs(file) {
		return "", false
	}
	if filepath.Clean(file) != filepath.FromSlash(file) || !samePath(filepath.Dir(file), dir) {
		return "", false
	}
	raw, err := readFile(file)
	if err != nil || !isOneVersionOneRequest(string(raw)) {
		return "", false
	}
	return string(raw), true
}
