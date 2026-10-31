package main

import (
	"bytes"
	"encoding/json"
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

func TestTheClaudeCodeNoteSendsAQuoteOrDollarThroughARequestFile(t *testing.T) {
	entry := oneLineEntry("linux")
	dir := requestsDirFor(entry.cfg)
	skill := flat(renderedSkillFor("claude", entry, "linux"))
	for _, want := range []string{
		"the call stays on one line",
		"A request holding a `'` or a `$` cannot go on that line:",
		"write it with the Write tool to a new file in `" + dir + "` (a new name each time)",
		"`--stdin < '" + requestFilePath(dir, "q-4821.json") + "'` instead of `--stdin <<< '…'`",
		"Never pipe or redirect the call's output",
		"A fetch holding a `'` or a `$` uses a request file too.",
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("the skill does not say %q", want)
		}
	}
	// The JSON-escape rule is gone: the model decoded the escapes before it
	// sent the call, so the call was denied anyway.
	for _, gone := range []string{`\u0027`, `\u0024`, "The heredoc delimiter is quoted"} {
		if strings.Contains(skill, gone) {
			t.Errorf("Claude Code's skill still carries %q", gone)
		}
	}
}

func TestOnlyClaudeCodesSkillNamesTheRequestsFolder(t *testing.T) {
	entry := oneLineEntry("linux")
	dir := requestsDirFor(entry.cfg)
	for _, tg := range installTargets {
		skill := renderedSkillFor(tg.ID(), entry, "linux")
		if has := strings.Contains(skill, dir); has != (tg.ID() == "claude") {
			t.Errorf("%s: the requests folder is named in its skill: %v", tg.ID(), has)
		}
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
	if recognizeClaudeForm(cmd, f.exe, f.entry.cfg, f.shells, nil) == nil {
		t.Error("Claude Code's recognizer does not take the here-string")
	}
}

// The file form's call, as the note teaches it: the same command with the
// request read from a file in the requests folder.
func fileFormCall(t *testing.T, e binEntry, sub, file string) string {
	t.Helper()
	build := e.stdinCommandForShell
	if sub == "fetch" {
		build = e.fetchCommandForShell
	}
	cmd, err := build(shellPOSIX)
	if err != nil {
		t.Fatal(err)
	}
	return cmd + " < " + posixQuoteArg(file)
}

// Claude Code needs two rules for the file form, and not the ones a reader
// would guess: its Write tool is governed by Edit rules (a Write rule allowed
// nothing, seen on 2.1.312), and the call's `<` file is checked against Read
// rules. Both name the folder and nothing above it, and only Claude Code's
// installation writes them.
func TestTheRequestsFolderGetsAnEditRuleAndAReadRuleAndNoWriteRule(t *testing.T) {
	entry := oneLineEntry("linux")
	want := claudeRulePath(requestsDirFor(entry.cfg)) + "/**"
	var got []string
	for _, r := range claudeAllowRules(entry) {
		if !strings.HasPrefix(r, "Bash(") {
			got = append(got, r)
		}
	}
	if len(got) != 2 || got[0] != "Edit("+want+")" || got[1] != "Read("+want+")" {
		t.Errorf("the folder's rules are %q, want Edit and Read for %s", got, want)
	}
	if !strings.HasPrefix(want, "//") {
		t.Errorf("the rule path %q is not the absolute form Claude Code reads", want)
	}
	if rules := claudeRequestRules(binEntry{command: entry.command}); rules != nil {
		t.Errorf("an entry with no config got rules for a folder: %q", rules)
	}
}

func TestOnlyClaudeCodesInstallWritesTheRequestFolderRules(t *testing.T) {
	m, ops := newFakeMachine("claude", "codex", "cursor")
	entry := installedEntry(t, ops)
	if code, out, errOut := runAgents(t, ops, nil, "install", "-yes", "-config", testCfg); code != exitOK {
		t.Fatalf("install: exit %d\n%s\n%s", code, out, errOut)
	}
	rule := "Edit(" + claudeRulePath(requestsDirFor(entry.cfg)) + "/**)"
	for path, b := range m.files {
		if has := strings.Contains(string(b), rule); has != (path == claudeSettingsPath) {
			t.Errorf("%s holds the folder's rule: %v", path, has)
		}
	}
}

func TestAnInstallFromBeforeTheRequestFolderGainsItsRulesAndUninstallTakesThemBack(t *testing.T) {
	m, ops := newFakeMachine("claude")
	entry := installedEntry(t, ops)
	var bash, folder []string
	for _, r := range claudeAllowRules(entry) {
		if strings.HasPrefix(r, "Bash(") {
			bash = append(bash, r)
		} else {
			folder = append(folder, r)
		}
	}
	other := binEntry{command: entry.command, cfg: foreignCfg(entry)}
	foreign := claudeRequestRules(other)
	seedAllow(t, m, append(append([]string{"Bash(git status:*)"}, bash...), foreign...)...)

	if code, out, errOut := runAgents(t, ops, nil, "install", "-yes", "-config", testCfg, "-client", "claude"); code != exitOK {
		t.Fatalf("install: exit %d\n%s\n%s", code, out, errOut)
	}
	allow := allowOf(t, m, claudeSettingsPath)
	for _, r := range folder {
		if countString(allow, r) != 1 {
			t.Errorf("after the install, %s is there %d times: %q", r, countString(allow, r), allow)
		}
	}
	for _, r := range foreign {
		if countString(allow, r) != 1 {
			t.Errorf("another installation's folder rule was disturbed: %s in %q", r, allow)
		}
	}
	if code, out, errOut := runAgents(t, ops, nil, "uninstall", "-yes", "-config", testCfg, "-client", "claude"); code != exitOK {
		t.Fatalf("uninstall: exit %d\n%s\n%s", code, out, errOut)
	}
	allow = allowOf(t, m, claudeSettingsPath)
	for _, r := range folder {
		if countString(allow, r) != 0 {
			t.Errorf("uninstall left this installation's folder rule %s", r)
		}
	}
	for _, r := range foreign {
		if countString(allow, r) != 1 {
			t.Errorf("uninstall took another installation's folder rule %s: %q", r, allow)
		}
	}
}

func hookWithFiles(t *testing.T, f recognizerFixture, command string, files map[string]string) hookAnswer {
	t.Helper()
	fs, ops := newFakeHookOps(nil)
	for p, c := range files {
		fs.files[p] = []byte(c)
	}
	ops.executable = f.exe
	payload, _ := json.Marshal(map[string]any{
		"session_id": "probe-session", "prompt_id": "probe-prompt", "tool_use_id": "probe-call",
		"tool_name": "Bash", "tool_input": map[string]any{"command": command},
	})
	var out bytes.Buffer
	hookLineage(ops, hookContext{cfgPath: f.entry.cfg}, payload, &out)
	got := hookAnswer{raw: out.String()}
	if strings.TrimSpace(got.raw) == "" {
		return got
	}
	var body struct {
		HookSpecificOutput map[string]any `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("not a hook answer: %v (%q)", err, got.raw)
	}
	got.Output = body.HookSpecificOutput
	if ui, ok := body.HookSpecificOutput["updatedInput"].(map[string]any); ok {
		got.command, _ = ui["command"].(string)
	}
	return got
}

// A search sent from a request file is labeled exactly like one sent in the
// here-string: allowed, and rewritten to carry the trace bridge.
func TestTheHookLabelsTheFileFormLikeTheHereString(t *testing.T) {
	f := newHookFixture(t, shellPOSIX, "jevlin.toml")
	file := filepath.Join(requestsDirFor(f.entry.cfg), "q-1.json")
	files := map[string]string{file: `{"version":1,"query":"what's the $HOME price; ok"}`}
	for _, sub := range []string{"search", "fetch"} {
		cmd := fileFormCall(t, f.entry, sub, file)
		got := hookWithFiles(t, f, cmd, files)
		if got.decision() != "allow" {
			t.Errorf("the file-form %s is answered %q\n%s", sub, got.decision(), got.raw)
		}
		if sub == "search" && !got.rewritten() {
			t.Errorf("the file-form search carries no trace bridge, so it runs without lineage\n%s", got.raw)
		}
		if sub == "fetch" && got.command != "" {
			t.Errorf("the fetch was rewritten: %s", got.command)
		}
	}
	// The same search by the here-string gets the same answer.
	_, here, _ := oneLineSearchBlock(f.entry, `{"version":1,"query":"probe"}`)
	a, b := hookWithFiles(t, f, here, nil), hookWithFiles(t, f, fileFormCall(t, f.entry, "search", file), files)
	if a.decision() != b.decision() || a.rewritten() != b.rewritten() {
		t.Errorf("the two forms are labeled differently: %q/%v and %q/%v", a.decision(), a.rewritten(), b.decision(), b.rewritten())
	}
}

// The hook reads a file only when the call names one inside the folder, and
// recognizes the call only when what it holds is one version-1 request.
func TestTheHookIgnoresAFileFormOutsideTheFolderOrHoldingNoRequest(t *testing.T) {
	f := newHookFixture(t, shellPOSIX, "jevlin.toml")
	dir := requestsDirFor(f.entry.cfg)
	good := `{"version":1,"query":"q"}`
	in := filepath.Join(dir, "q-1.json")
	for _, tc := range []struct {
		name, file string
		files      map[string]string
	}{
		{"a file beside the folder", filepath.Join(filepath.Dir(dir), "q-1.json"), map[string]string{filepath.Join(filepath.Dir(dir), "q-1.json"): good}},
		{"a file in a subfolder", filepath.Join(dir, "sub", "q-1.json"), map[string]string{filepath.Join(dir, "sub", "q-1.json"): good}},
		{"a path climbing out of the folder", dir + "/../q-1.json", map[string]string{filepath.Join(filepath.Dir(dir), "q-1.json"): good}},
		{"a relative path", "requests/q-1.json", map[string]string{"requests/q-1.json": good}},
		{"a file that is not there", in, nil},
		{"a body with no version", in, map[string]string{in: `{"query":"q"}`}},
		{"a body with a second object", in, map[string]string{in: good + good}},
		{"a body that is not JSON", in, map[string]string{in: "hello"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hookWithFiles(t, f, fileFormCall(t, f.entry, "search", tc.file), tc.files); got.decision() != "" {
				t.Errorf("answered %q for %s", got.decision(), tc.file)
			}
		})
	}
	t.Run("a second statement after the redirect", func(t *testing.T) {
		cmd := fileFormCall(t, f.entry, "search", in) + "; echo x"
		if got := hookWithFiles(t, f, cmd, map[string]string{in: good}); got.decision() != "" {
			t.Errorf("answered %q for %s", got.decision(), cmd)
		}
	})
	t.Run("the output piped on", func(t *testing.T) {
		cmd := fileFormCall(t, f.entry, "search", in) + " | head"
		if got := hookWithFiles(t, f, cmd, map[string]string{in: good}); got.decision() != "" {
			t.Errorf("answered %q for %s", got.decision(), cmd)
		}
	})
	t.Run("only Claude Code's recognizer takes it", func(t *testing.T) {
		cmd := fileFormCall(t, f.entry, "search", in)
		if recognizeRenderedForm(cmd, f.exe, f.entry.cfg, f.shells) != nil {
			t.Error("the shared recognizer takes the file form")
		}
	})
}

// The file form must deliver the request exactly, under the shells Claude
// Code's Bash tool runs, including the characters that cannot ride on the
// line: a quote and a dollar sign.
func TestTheFileFormDeliversTheRequestExactlyUnderBashAndZsh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows runners' Git Bash is covered by the POSIX-shell exec tests")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, ".jevlin-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat\n"), 0o700); err != nil { //nolint:gosec // a test stub that must be executable
		t.Fatal(err)
	}
	entry := binEntry{command: stub, cfg: filepath.Join(dir, "jevlin.toml")}
	if err := os.MkdirAll(requestsDirFor(entry.cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"query":"what's the $HOME price; ok ` + "`x`" + ` $(y) | z > w \\n é 中"}`
	file := filepath.Join(requestsDirFor(entry.cfg), "q-1.json")
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"bash", "zsh"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			if shell == "bash" {
				t.Fatalf("no bash: %v", err)
			}
			continue
		}
		out, err := exec.Command(path, "-c", fileFormCall(t, entry, "search", file)).Output() //nolint:gosec // the shell and command are this test's own
		if err != nil {
			t.Fatalf("%s: %v", shell, err)
		}
		if string(out) != body {
			t.Errorf("%s changed the request:\n got %q\nwant %q", shell, out, body)
		}
	}
}

// The file form is covered by the same Bash prefix rules as the here-string,
// for search and for fetch, and the rule matcher refuses it once the request
// is not read from a file.
func TestTheFileFormIsCoveredByTheInstalledBashRules(t *testing.T) {
	entry := oneLineEntry("linux")
	rules := claudeAllowRules(entry)
	file := filepath.Join(requestsDirFor(entry.cfg), "q-1.json")
	for _, sub := range []string{"search", "fetch"} {
		if cmd := fileFormCall(t, entry, sub, file); !allowRuleCovers(rules, cmd) {
			t.Errorf("no installed allow rule covers %s", cmd)
		}
	}
}
