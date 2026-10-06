package main

// A command this client's hooks allow must be exactly the command its skill
// renders, and a path in it exactly one quoted word.
//
// The recognizer reads a path region as everything up to the next literal of
// the rendered form, so a region need not be one word. With the config's
// place holding `'/x'; echo x; '/../jevlin.toml'`, the region cleaned to the
// right config path, and Claude Code's lineage hook and Cursor's
// beforeShellExecution both allowed a command a POSIX shell runs as two.
// readRenderedPath now requires each region to be what the shell's own
// quoting writes for the path it names, and the config to be in clean form.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// injections are region contents that close the quoted word and add a
// command, for each shell's quoting. Every added command is harmless.
var injections = map[shellKind][]string{
	shellPOSIX: {
		`/home/u/.jevlin/x'; echo x; '/../jevlin.toml`,
		`/home/u/.jevlin/x' && echo x && '/../jevlin.toml`,
		`/home/u/.jevlin/x'$(echo x)'/../jevlin.toml`,
	},
	shellPowerShell: {
		`C:\Users\u\x'; echo x; '\..\jevlin.toml`,
		"C:\\Users\\u\\x\u2019; echo x; \u2018\\..\\jevlin.toml",
	},
}

func TestARenderedPathRegionIsExactlyOneQuotedWord(t *testing.T) {
	for _, sh := range []shellKind{shellPOSIX, shellPowerShell} {
		bin, cfg := "/home/u/.jevlin/bin/jevlin", "/home/u/.jevlin/jevlin.toml"
		if sh == shellPowerShell {
			bin, cfg = `C:\Users\u\.jevlin\bin\jevlin.exe`, `C:\Users\u\.jevlin\jevlin.toml`
		}
		quote := posixQuoteArg
		if sh == shellPowerShell {
			quote = powerShellQuoteArg
		}
		_, search, err := searchBlockForShell(sh, binEntry{command: bin, cfg: cfg}, `{"version":1,"query":"q"}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := matchedRenderedForms(search, cfg, []shellKind{sh}); len(got) != 1 || got[0].bin != bin || got[0].cfg != cfg {
			t.Fatalf("%s: the rendered search itself is not read back as rendered: %+v", sh, got)
		}
		for _, inj := range injections[sh] {
			for region, from := range map[string]string{"config": quote(cfg), "binary": quote(bin)} {
				command := strings.Replace(search, from, "'"+inj+"'", 1)
				if command == search {
					t.Fatalf("%s %s: the injection was not applied", sh, region)
				}
				if got := matchedRenderedForms(command, cfg, []shellKind{sh}); len(got) != 0 {
					t.Errorf("%s: a %s region carrying a second command was read as one path: %q -> %+v", sh, region, inj, got)
				}
			}
		}
		// Paths that really hold a quote, written the way the shell's own
		// quoting writes them, still read back to themselves.
		for _, real := range []string{"/home/it's/.jevlin/jevlin.toml", "/home/u/a''b/jevlin.toml"} {
			if sh == shellPowerShell {
				real = strings.ReplaceAll(real, "/", `\`)
			}
			_, s, err := searchBlockForShell(sh, binEntry{command: bin, cfg: real}, `{"version":1,"query":"q"}`)
			if err != nil {
				t.Fatal(err)
			}
			if got := matchedRenderedForms(s, real, []shellKind{sh}); len(got) != 1 || got[0].cfg != real {
				t.Errorf("%s: a config path holding a quote is not read back: %q -> %+v", sh, real, got)
			}
		}
	}
	// cmd carries a path in double quotes, which no Windows path holds; one in
	// the region closes the word.
	bin, cfg := `C:\Users\u\.jevlin\bin\jevlin.exe`, `C:\Users\u\.jevlin\jevlin.toml`
	prefer, err := binEntry{command: bin, cfg: cfg}.preferCommandForShell(shellCmd)
	if err != nil {
		t.Fatal(err)
	}
	if got := matchedRenderedForms(prefer+" on", cfg, []shellKind{shellCmd}); len(got) != 1 {
		t.Fatalf("cmd: the rendered prefer command is not read back: %+v", got)
	}
	injected := strings.Replace(prefer+" on", `"`+cfg+`"`, `"C:\x" & echo x & "C:\Users\u\.jevlin\jevlin.toml"`, 1)
	if got := matchedRenderedForms(injected, cfg, []shellKind{shellCmd}); len(got) != 0 {
		t.Errorf("cmd: a config region carrying a second command was read as one path: %+v", got)
	}
}

// exactTestInstall is a regular file named jevlin, which is what the
// identity check reads, and this installation's config path.
func exactTestInstall(t *testing.T) (bin, cfg, search string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "bin", "jevlin")
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("stand-in\n"), 0o700); err != nil { // #nosec G306 -- the test's own stand-in executable
		t.Fatal(err)
	}
	cfg = filepath.Join(dir, "jevlin.toml")
	_, search, err := searchBlockForShell(shellPOSIX, binEntry{command: bin, cfg: cfg}, `{"version":1,"query":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	return bin, cfg, search
}

// Through the three hooks that answer "allow": Claude Code's lineage,
// Cursor's beforeShellExecution and Codex's PreToolUse, the last for Linux,
// whose Codex runs the POSIX form. The rendered search is allowed (the
// control); the same search with a config or binary region that closes its
// quoted word is not.
func TestTheAllowingHooksRefuseASearchCarryingASecondCommand(t *testing.T) {
	bin, cfg, search := exactTestInstall(t)
	cases := map[string]string{"the rendered search": search}
	for i, inj := range injections[shellPOSIX] {
		inj = strings.ReplaceAll(inj, "/home/u/.jevlin", filepath.Dir(cfg))
		cases["config "+string(rune('a'+i))] = strings.Replace(search, posixQuoteArg(cfg), "'"+inj+"'", 1)
	}
	// A directory whose name closes the quoted word: the region then names
	// the real binary through it, so only the quoting can refuse it.
	sep := string(filepath.Separator)
	odd := filepath.Join(filepath.Dir(bin), "x'; echo x; '")
	if err := os.MkdirAll(odd, 0o700); err != nil {
		t.Skipf("this filesystem will not hold the directory name: %v", err)
	}
	cases["binary"] = strings.Replace(search, posixQuoteArg(bin), "'"+odd+sep+".."+sep+"jevlin'", 1)
	// The same, written so the bridge's placement rule reads the line as
	// leading with this binary's search: the case that reaches Claude Code's
	// allow, which the one above stops short of.
	lead := filepath.Join(filepath.Dir(filepath.Dir(bin)), "jevlin' search ; echo x ; '")
	if err := os.MkdirAll(lead, 0o700); err != nil {
		t.Skipf("this filesystem will not hold the directory name: %v", err)
	}
	cases["binary, leading with a search"] = strings.Replace(search, posixQuoteArg(bin), "'"+lead+sep+".."+sep+"bin"+sep+"jevlin'", 1)
	for name, command := range cases {
		allowed := name == "the rendered search"
		_, ops := newFakeHookOps(nil)
		ops.executable = func() (string, error) { return bin, nil }

		var out bytes.Buffer
		hookLineage(ops, hookContext{cfgPath: cfg}, mustJSON(t, map[string]any{
			"session_id": "s", "tool_name": "Bash", "tool_input": map[string]any{"command": command},
		}), &out)
		if got := strings.Contains(out.String(), `"permissionDecision":"allow"`); got != allowed {
			t.Errorf("Claude Code's lineage hook, %s: allowed %v, want %v\n%s", name, got, allowed, out.String())
		}
		if got := recognizeCursorCommand(ops, hookContext{cfgPath: cfg}, command, []shellKind{shellPOSIX}) != nil; got != allowed {
			t.Errorf("Cursor's beforeShellExecution, %s: allowed %v, want %v", name, got, allowed)
		}
		out.Reset()
		hookCodexOn(ops, hookContext{cfgPath: cfg}, "linux", "PreToolUse", mustJSON(t, map[string]any{
			"session_id": "s", "tool_name": "Bash", "tool_input": map[string]any{"command": command},
		}), &out)
		if got := strings.Contains(out.String(), `"permissionDecision":"allow"`); got != allowed {
			t.Errorf("Codex's PreToolUse, %s: allowed %v, want %v\n%s", name, got, allowed, out.String())
		}
	}
}

// The config in an allowed command must already be in clean form: `a/..`
// cleans away in text whatever `a` is, while the kernel follows `a` when it
// is a symlink and opens another file.
func TestARecognizedConfigPathIsAlreadyClean(t *testing.T) {
	bin, cfg, search := exactTestInstall(t)
	dir := filepath.Dir(cfg)
	if err := os.Symlink(filepath.Join(dir, "bin"), filepath.Join(dir, "a")); err != nil {
		t.Skipf("this filesystem will not hold a symlink: %v", err)
	}
	// Cleaned as text, this is the installation's config; opened, it is
	// <dir>/jevlin.toml only if `a` were a plain directory under <dir>, and
	// `a` points into <dir>/bin.
	dotted := filepath.Join(dir, "a") + string(filepath.Separator) + ".." + string(filepath.Separator) + "jevlin.toml"
	command := strings.Replace(search, posixQuoteArg(cfg), posixQuoteArg(dotted), 1)
	if command == search {
		t.Fatal("the dotted config was not applied")
	}
	if f := recognizeRenderedForm(command, func() (string, error) { return bin, nil }, cfg, []shellKind{shellPOSIX}); f != nil {
		t.Errorf("a config path that only cleans to this installation's was recognized: %s", dotted)
	}
	if f := recognizeRenderedForm(search, func() (string, error) { return bin, nil }, cfg, []shellKind{shellPOSIX}); f == nil {
		t.Fatal("the control was not recognized, so the refusal above proves nothing")
	}
}

// A config path spelled with forward slashes is clean: on Windows `/` is a
// separator, and Clean's rewriting it to `\` is not a `.` or `..` element.
// Elsewhere ToSlash changes nothing and this is the control again; the
// Windows runners are where it holds anything.
func TestAConfigPathSpelledWithForwardSlashesIsStillClean(t *testing.T) {
	bin, cfg, search := exactTestInstall(t)
	slashed := filepath.ToSlash(cfg)
	command := strings.Replace(search, posixQuoteArg(cfg), posixQuoteArg(slashed), 1)
	if command == search && slashed != cfg {
		t.Fatal("the slashed config was not applied")
	}
	if f := recognizeRenderedForm(command, func() (string, error) { return bin, nil }, cfg, []shellKind{shellPOSIX}); f == nil {
		t.Errorf("a config path spelled with forward slashes was refused: %s", slashed)
	}
	dotted := filepath.ToSlash(filepath.Join(filepath.Dir(cfg), "bin")) + "/../jevlin.toml"
	if f := recognizeRenderedForm(strings.Replace(search, posixQuoteArg(cfg), posixQuoteArg(dotted), 1), func() (string, error) { return bin, nil }, cfg, []shellKind{shellPOSIX}); f != nil {
		t.Errorf("a slashed config path with a .. element was recognized: %s", dotted)
	}
}

// reencodedByCursorOnWindows is what Cursor's Windows hook wrapper hands a
// hook (dropin-miner#113): each UTF-8 byte of the payload read as one cp1252
// character, then written out as UTF-8 again.
func reencodedByCursorOnWindows(s string) string {
	high := map[byte]rune{}
	for r, b := range cp1252High {
		high[b] = r
	}
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if r, ok := high[s[i]]; ok {
			out.WriteRune(r)
			continue
		}
		out.WriteRune(rune(s[i]))
	}
	return out.String()
}

// On Windows Cursor's hook reads the command after its runner re-encoded it,
// so a typographic quote PowerShell ends a string at reaches the hook as
// three characters nothing refuses. The real bytes are refused; the bytes
// the hook sees must be too. The rendered search, and one whose query is not
// ASCII, are still allowed. Driven through the event, with Windows' declared
// shells and runners, so the answer Cursor reads is what is asserted.
func TestCursorShellHookRefusesAPathItsRunnerReencoded(t *testing.T) {
	bin, cfg, _ := exactTestInstall(t)
	shells, err := declaredShells(cursorTarget{}, "windows", channelTool)
	if err != nil {
		t.Fatal(err)
	}
	render := func(body string) string {
		_, s, err := searchBlockForShell(shellPowerShell, binEntry{command: bin, cfg: cfg}, body)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	search := render(`{"version":1,"query":"q"}`)
	// The directory the hook's view names exists and holds this binary: a
	// cloned repository can hold the directory, and a hard link needs no
	// privilege on any of the three systems.
	segment := "d\u2019; echo x; & \u2018"
	seenDir := filepath.Join(filepath.Dir(bin), reencodedByCursorOnWindows(segment))
	if err := os.MkdirAll(seenDir, 0o700); err != nil {
		t.Skipf("this filesystem will not hold the directory name: %v", err)
	}
	if err := os.Link(bin, filepath.Join(seenDir, "jevlin")); err != nil {
		t.Skipf("this filesystem will not hold a hard link: %v", err)
	}
	real := strings.Replace(search, powerShellQuoteArg(bin), "'"+filepath.Join(filepath.Dir(bin), segment, "jevlin")+"'", 1)
	if real == search {
		t.Fatal("the injection was not applied")
	}
	if got := matchedRenderedForms(real, cfg, []shellKind{shellPowerShell}); len(got) != 0 {
		t.Fatalf("the bytes PowerShell runs are not refused, so this case is not the one it says: %+v", got)
	}
	_, ops := newFakeHookOps(nil)
	ops.executable = func() (string, error) { return bin, nil }
	hc := hookContext{cfgPath: cfg}
	seen := reencodedByCursorOnWindows(real)
	if recognizeCursorCommand(ops, hc, seen, shells) == nil {
		t.Fatal("the recognizer alone refuses the re-encoded command, so the runner rule is not what is tested")
	}
	answer := func(command string) string {
		var out bytes.Buffer
		hookCursorOn("windows", ops, hc, "beforeShellExecution", mustJSON(t, map[string]any{"command": command}), &out, io.Discard)
		return out.String()
	}
	if got := answer(seen); got != "" {
		t.Errorf("Cursor's beforeShellExecution answered %q to a command whose real bytes PowerShell runs as several statements:\nreal: %q\nseen: %q", got, real, seen)
	}
	for name, command := range map[string]string{
		"the rendered search":               search,
		"a search whose query is not ASCII": render(`{"version":1,"query":"caf\u00e9 \u2014 na\u00efve"}`),
	} {
		if got := answer(reencodedByCursorOnWindows(command)); got != `{"permission":"allow"}`+"\n" {
			t.Errorf("%s is no longer allowed under Windows' hook runners: answered %q", name, got)
		}
	}
}

// On Windows a binary path holding a . or .. element can name one file to
// sameBinary and run another, and so can one Windows reads as . or .. once it
// strips the dots and spaces that end an element; none is taken there.
// Elsewhere the kernel resolves the path as sameBinary does, and nothing
// changes.
func TestABinaryPathWithADotSegmentIsNotTakenOnWindows(t *testing.T) {
	for _, c := range []struct {
		goos, path string
		want       bool
	}{
		{"windows", `C:\Users\u\.jevlin\bin\jevlin.exe`, true},
		{"windows", `C:\X\link\..\bin\jevlin.exe`, false},
		{"windows", `C:\X\.\bin\jevlin.exe`, false},
		{"windows", `C:/X/link/../bin/jevlin.exe`, false},
		{"windows", `C:\X\link\...\bin\jevlin.exe`, false},
		{"windows", `C:\X\link\.. \bin\jevlin.exe`, false},
		{"windows", `C:\X\link\. .\bin\jevlin.exe`, false},
		{"windows", `C:\X\bin \jevlin.exe`, false},
		{"windows", `C:\X\bin\jevlin.exe.`, false},
		{"windows", `C:\Users\u\..jevlin\bin\jevlin.exe`, true},
		{"windows", `C:\Program Files\jevlin\jevlin.exe`, true},
		{"darwin", "/Users/u/work/../.jevlin/bin/jevlin", true},
		{"linux", "/home/u/link/../bin/jevlin", true},
	} {
		if got := binaryPathRunsAsRead(c.goos, c.path); got != c.want {
			t.Errorf("binaryPathRunsAsRead(%s, %q) = %v, want %v", c.goos, c.path, got, c.want)
		}
	}
}
