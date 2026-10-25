package main

// The fetch the skill renders runs without a permission prompt in the three
// hosts that let the search run without one, and nothing broader does: the
// exact rendered command, naming this installation's binary and config, for
// the shell that will run it. A fetch carries no trace, so none of the hooks
// rewrites it or writes a lineage file for it.

import (
	"bytes"
	"encoding/json"
	"io"
	"runtime"
	"strings"
	"testing"
)

const fetchAllowBody = `{"version":1,"pages":[{"source_id":"s_0123456789abcdef0123456789abcdef"}]}`

// renderedFetch is the fetch this fixture's skill renders for sh.
func (f recognizerFixture) renderedFetch(t *testing.T, sh shellKind, body string) string {
	t.Helper()
	_, script, err := fetchBlockForShell(sh, f.entry, body)
	if err != nil {
		t.Fatal(err)
	}
	return script
}

// notQuiteTheFetch are commands one step from the rendered fetch, each of
// which must get no allow.
func notQuiteTheFetch(t *testing.T, fetch, body string, other recognizerFixture, sh shellKind) map[string]string {
	t.Helper()
	if !strings.Contains(fetch, " --stdin") {
		t.Fatalf("the rendered fetch has no --stdin to follow:\n%s", fetch)
	}
	return map[string]string{
		"a -config after its prefix":     strings.Replace(fetch, " --stdin", " --stdin -config /tmp/other.toml", 1),
		"a second statement after it":    fetch + "\necho x",
		"a command chained onto its end": fetch + " && echo x",
		"another installation's config":  other.renderedFetch(t, sh, body),
		"a body that is not one request": strings.Replace(fetch, body, `{"version":2,"pages":[]}`, 1),
	}
}

// Claude Code: the lineage hook answers the rendered fetch with an allow and
// leaves the command as it is, for the Bash tool and for the PowerShell tool,
// which the installed Bash(...) rules cannot name.
func TestClaudeAllowsTheRenderedFetchAsIs(t *testing.T) {
	for _, sh := range []shellKind{shellPOSIX, shellPowerShell} {
		t.Run(string(sh), func(t *testing.T) {
			f := newHookFixture(t, sh, "jevlin.toml")
			fetch := f.renderedFetch(t, sh, fetchAllowBody)
			got := askHook(t, f, toolFor(sh), fetch)
			if got.decision() != "allow" {
				t.Fatalf("the rendered fetch is answered %q, want allow\nanswer: %s", got.decision(), got.raw)
			}
			if _, has := got.Output["updatedInput"]; has {
				t.Errorf("the fetch was rewritten: %s", got.raw)
			}
			if _, ok := got.Output["permissionDecisionReason"].(string); !ok {
				t.Errorf("the allow carries no reason: %s", got.raw)
			}
			// The wrong tool's shell is not this command's shell.
			if wrong := askHook(t, f, toolFor(map[shellKind]shellKind{shellPOSIX: shellPowerShell, shellPowerShell: shellPOSIX}[sh]), fetch); wrong.raw != "" {
				t.Errorf("answered a fetch rendered for %s under the other tool: %s", sh, wrong.raw)
			}
			other := newHookFixture(t, sh, "other.toml")
			for name, cmd := range notQuiteTheFetch(t, fetch, fetchAllowBody, other, sh) {
				if got := askHook(t, f, toolFor(sh), cmd); got.decision() == "allow" {
					t.Errorf("%s: allowed\n%s", name, cmd)
				}
			}
		})
	}
}

// Codex: the rendered fetch gets an allow with the tool's input echoed
// unchanged, the one answer shape a live run established, and no file.
func TestCodexAllowsTheRenderedFetchAsIs(t *testing.T) {
	in := newCodexTestInstall(t, "/home/u/.jevlin/jevlin.toml")
	_, fetch, err := fetchBlockForShell(shellPOSIX, in.entry(), fetchAllowBody)
	if err != nil {
		t.Fatal(err)
	}
	payload := withCommand(codexPayloadFixture(t, "codex-0.158.0-linux-PreToolUse-bash.json"), fetch)
	payload["tool_input"].(map[string]any)["workdir"] = "/tmp/x"
	for _, goos := range []string{"darwin", "linux"} {
		fs, ops := in.ops(nil)
		out := runCodexOn(t, ops, hcFor(in), goos, "PreToolUse", payload)
		if cmd := rewrittenCommand(t, out); cmd != fetch {
			t.Errorf("%s: the fetch came back changed:\n%s", goos, cmd)
		}
		var a codexAnswer
		_ = json.Unmarshal([]byte(out), &a)
		var input map[string]any
		_ = json.Unmarshal(a.Out["updatedInput"], &input)
		if input["workdir"] != "/tmp/x" || len(input) != 2 {
			t.Errorf("%s: the tool's input did not come back as it went: %v", goos, input)
		}
		if len(fs.files) != 0 {
			t.Errorf("%s: a fetch wrote %v", goos, keys(fs.files))
		}
	}

	other := newCodexTestInstall(t, "/home/u/dm-disposable/jevlin.toml")
	_, otherFetch, _ := fetchBlockForShell(shellPOSIX, other.entry(), fetchAllowBody)
	for name, cmd := range map[string]string{
		"a -config after its prefix":     strings.Replace(fetch, " --stdin", " --stdin -config /tmp/other.toml", 1),
		"a second statement after it":    strings.Replace(fetch, "\nJSON", "\nJSON\necho x", 1),
		"a command chained onto its end": fetch + " && echo x",
		"another installation's fetch":   strings.Replace(otherFetch, other.bin, in.bin, 1),
		"the fetch after cd":             "cd /tmp && " + fetch,
		"a fetch by pattern only":        "jevlin fetch --stdin < req.json",
	} {
		t.Run(name, func(t *testing.T) {
			fs, ops := in.ops(nil)
			if out := runCodexOn(t, ops, hcFor(in), "linux", "PreToolUse", withCommand(codexPayloadFixture(t, "codex-0.158.0-linux-PreToolUse-bash.json"), cmd)); out != "" || len(fs.files) != 0 {
				t.Fatalf("answered %q and wrote %v", out, keys(fs.files))
			}
		})
	}
}

// Cursor: beforeShellExecution allows the rendered fetch for each shell
// Cursor runs on this OS, and stamps nothing: the call is not a search.
func TestCursorAllowsTheRenderedFetch(t *testing.T) {
	shells, err := declaredShells(cursorTarget{}, runtime.GOOS, channelTool)
	if err != nil {
		t.Skipf("Cursor declares no tool shell on %s", runtime.GOOS)
	}
	hc := hookContext{cfgPath: "/c.toml", sessionsDir: "/sessions"}
	entry := binEntry{command: mustExecutable(t), cfg: hc.cfgPath}
	for _, sh := range shells {
		t.Run(string(sh), func(t *testing.T) {
			_, fetch, err := fetchBlockForShell(sh, entry, fetchAllowBody)
			if err != nil {
				t.Fatal(err)
			}
			ask := func(command string) string {
				fs, ops := newFakeHookOps(nil)
				var out bytes.Buffer
				hookCursor(ops, hc, "beforeShellExecution", mustJSON(t, map[string]any{
					"conversation_id": "conv-1", "generation_id": "gen-1", "workspace_roots": []string{"/w/proj"},
					"cursor_version": "3.21.16", "command": command,
				}), &out, io.Discard)
				if len(fs.files) != 0 {
					t.Errorf("a fetch stamped %v", keys(fs.files))
				}
				return strings.TrimSpace(out.String())
			}
			if got := ask(fetch); got != `{"permission":"allow"}` {
				t.Fatalf("beforeShellExecution answered %q for the rendered fetch", got)
			}
			if got := ask(fetch + " && echo x"); got != "" {
				t.Errorf("answered %q for the fetch with a command chained on", got)
			}
			other := binEntry{command: entry.command, cfg: "/other.toml"}
			_, otherFetch, _ := fetchBlockForShell(sh, other, fetchAllowBody)
			if got := ask(otherFetch); got != "" {
				t.Errorf("answered %q for another installation's fetch", got)
			}
		})
	}
}
