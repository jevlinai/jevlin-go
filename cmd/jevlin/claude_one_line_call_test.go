package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Claude Code matches a Bash command against the allow rules install writes
// as one command line, and does not match a multi-line heredoc against a
// rule for its prefix. So its skill teaches the call as one line, the request
// in a single-quoted here-string, and every other host keeps the heredoc.

func oneLineEntry(goos string) binEntry {
	return binEntry{command: "/home/u/.jevlin/bin/jevlin", cfg: "/home/u/.jevlin/jevlin.toml"}
}

func TestClaudeCodeIsTaughtTheOneLineCallForSearchAndFetch(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			entry := oneLineEntry(goos)
			skill := renderedSkillFor("claude", entry, goos)
			var searches, fetches int
			for _, b := range skillCommandBlocks(skill) {
				if b.lang != "bash" || !strings.Contains(b.body, "--stdin") {
					continue
				}
				if strings.Contains(b.body, "\n") {
					t.Errorf("a stdin call in Claude Code's skill spans lines:\n%s", b.body)
				}
				if !strings.Contains(b.body, " --stdin <<< '{") {
					t.Errorf("a stdin call is not the one-line here-string:\n%s", b.body)
				}
				switch {
				case strings.Contains(b.body, " search "):
					searches++
				case strings.Contains(b.body, " fetch "):
					fetches++
				}
			}
			if searches != 1 || fetches != 1 {
				t.Errorf("want one search and one fetch call, got %d and %d", searches, fetches)
			}
			if strings.Contains(skill, "<<'JSON'") {
				t.Error("Claude Code's skill still teaches the heredoc")
			}
			if goos == "windows" && !strings.Contains(skill, "@'\n") {
				t.Error("the PowerShell form for Claude Code's PowerShell tool is gone")
			}
		})
	}
}

func TestEveryOtherHostKeepsTheHeredocCall(t *testing.T) {
	for _, tg := range installTargets {
		if tg.ID() == "claude" {
			continue
		}
		t.Run(tg.ID(), func(t *testing.T) {
			skill := renderedSkillFor(tg.ID(), oneLineEntry("linux"), "linux")
			if strings.Contains(skill, "<<<") {
				t.Errorf("%s was handed the one-line form", tg.ID())
			}
			if !strings.Contains(skill, "<<'JSON'") {
				t.Errorf("%s lost its heredoc call", tg.ID())
			}
		})
	}
}

func TestTheClaudeCodeQuotingNoteKeepsQuoteAndDollarOutOfTheRequest(t *testing.T) {
	skill := flat(renderedSkillFor("claude", oneLineEntry("linux"), "linux"))
	for _, want := range []string{
		`"what\u0027s new in Go"`,
		"replace every `'` in the request with `\\u0027` and every `$` with `\\u0024`",
		"the call stays on one line",
		"Never pipe or redirect the call's output",
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("the quoting note does not say %q", want)
		}
	}
	if strings.Contains(skill, "The heredoc delimiter is quoted") {
		t.Error("the heredoc note is still in Claude Code's skill")
	}
}

// allowRuleCovers is the match Claude Code makes for a prefix rule: the
// rule's command is a prefix of the command, which is one line.
func allowRuleCovers(rules []string, command string) bool {
	if strings.Contains(command, "\n") {
		return false
	}
	for _, r := range rules {
		inner, ok := strings.CutPrefix(r, "Bash(")
		if !ok {
			continue
		}
		prefix, ok := strings.CutSuffix(inner, ":*)")
		if ok && strings.HasPrefix(command, prefix) {
			return true
		}
	}
	return false
}

func TestTheOneLineCallIsCoveredByTheInstalledRulesWithAndWithoutAnApostrophe(t *testing.T) {
	entry := oneLineEntry("linux")
	rules := claudeAllowRules(entry)
	for _, body := range []string{
		`{"version":1}`,
		`{"version":1,"query":"what's new in Go","bogus":1}`,
		`{"version":1,"query":"it's 'quoted' ''"}`,
	} {
		_, search, err := oneLineSearchBlock(entry, body)
		if err != nil {
			t.Fatal(err)
		}
		_, fetch, err := oneLineFetchBlock(entry, body)
		if err != nil {
			t.Fatal(err)
		}
		for _, cmd := range []string{search, fetch} {
			if !allowRuleCovers(rules, cmd) {
				t.Errorf("no installed allow rule covers %s", cmd)
			}
		}
	}
	// The same check refuses the old form, so a test that always passed
	// would show here.
	_, heredoc, _ := searchBlockForShell(shellPOSIX, entry, `{"version":1}`)
	if allowRuleCovers(rules, heredoc) {
		t.Error("the matcher covers a multi-line heredoc; it would pass whatever the form")
	}
}

// The request has to arrive byte for byte under the shells Claude Code's
// Bash tool runs: bash, and zsh where it is the user's shell.
func TestTheOneLineCallDeliversTheRequestExactlyUnderBashAndZsh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows runners' Git Bash is covered by the POSIX-shell exec tests")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, ".jevlin-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat\n"), 0o700); err != nil { //nolint:gosec // a test stub that must be executable
		t.Fatal(err)
	}
	entry := binEntry{command: stub, cfg: filepath.Join(dir, "jevlin.toml")}
	bodies := []string{
		`{"version":1}`,
		`{"version":1,"query":"what's new in Go"}`,
		`{"version":1,"query":"a ''b'' $HOME ` + "`x`" + ` \"q\" \\n é 中"}`,
	}
	for _, shell := range []string{"bash", "zsh"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			if shell == "bash" {
				t.Fatalf("no bash: %v", err)
			}
			t.Logf("%s is not installed here; not run", shell)
			continue
		}
		for _, body := range bodies {
			_, cmd, err := oneLineSearchBlock(entry, body)
			if err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(path, "-c", cmd).Output() //nolint:gosec // the shell and command are this test's own
			if err != nil {
				t.Fatalf("%s: %v\n%s", shell, err, cmd)
			}
			if got := strings.TrimSuffix(string(out), "\n"); got != body {
				t.Errorf("%s changed the request:\n got %q\nwant %q", shell, got, body)
			}
		}
	}
}

// Claude Code's hook still recognizes the call its skill teaches, so the
// search is stamped with its lineage and allowed; and it recognizes only that
// call.
func TestTheHookRecognizesTheOneLineCallExactly(t *testing.T) {
	f := newHookFixture(t, shellPOSIX, "jevlin.toml")
	for _, body := range []string{`{"version":1,"query":"probe"}`, `{"version":1,"query":"what's new"}`} {
		_, cmd, err := oneLineSearchBlock(f.entry, body)
		if err != nil {
			t.Fatal(err)
		}
		got := askHook(t, f, "Bash", cmd)
		if got.decision() != "allow" || !got.rewritten() {
			t.Errorf("the one-line search is answered %q (rewritten %v)\n%s", got.decision(), got.rewritten(), got.raw)
		}
		_, fetch, _ := oneLineFetchBlock(f.entry, body)
		if got := askHook(t, f, "Bash", fetch); got.decision() != "allow" {
			t.Errorf("the one-line fetch is answered %q", got.decision())
		}
	}
	_, ok, _ := oneLineSearchBlock(f.entry, `{"version":1}`)
	for _, tc := range []struct{ name, cmd string }{
		{"a second statement after it", ok + "; echo x"},
		{"a quote ended early", strings.TrimSuffix(ok, "'") + `'; echo x; '`},
		{"an unescaped apostrophe", strings.Replace(ok, `{"version":1}`, `{"version":1,"query":"it's"}`, 1)},
		{"the body without a version", strings.Replace(ok, `{"version":1}`, `{"query":"q"}`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := askHook(t, f, "Bash", tc.cmd); got.decision() != "" {
				t.Errorf("answered %q for %s", got.decision(), tc.cmd)
			}
		})
	}
}

// Cursor and Codex do not recognize the here-string: their skills do not
// teach it, and a permission answer is for the exact rendered form only.
func TestOnlyClaudeCodesRecognizerTakesTheOneLineForm(t *testing.T) {
	f := newHookFixture(t, shellPOSIX, "jevlin.toml")
	_, cmd, _ := oneLineSearchBlock(f.entry, `{"version":1,"query":"probe"}`)
	if recognizeRenderedForm(cmd, f.exe, f.entry.cfg, f.shells) != nil {
		t.Error("the shared recognizer takes the here-string")
	}
	if recognizeClaudeForm(cmd, f.exe, f.entry.cfg, f.shells) == nil {
		t.Error("Claude Code's recognizer does not take the here-string")
	}
}
