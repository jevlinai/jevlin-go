package main

// JEVLIN_TRACE=off switches traces off entirely (traceOff, search.go), for
// Cursor's and Codex's hooks as for Claude Code's (hook_trace_off_test.go):
// they keep nothing of the session for a trace that will not be sent. Each
// case runs the same calls with tracing on first, so a fixture that never
// reached the label or the lineage write cannot pass for one that was
// stopped.

import (
	"bytes"
	"encoding/json"
	"io"
	"runtime"
	"strings"
	"testing"
)

// sessionsFiles is every file the fake filesystem holds under /sessions.
func sessionsFiles(fs *fakeHookFS) []string {
	var got []string
	for p := range fs.files {
		if strings.HasPrefix(p, "/sessions") {
			got = append(got, p)
		}
	}
	return got
}

// Cursor's hooks: with traces off no event writes the conversation's
// lineage file, so the user's prompt, the assistant's reasoning and its
// answer never reach the sessions directory, and preToolUse labels no
// search. beforeShellExecution and beforeSubmitPrompt answer as before.
func TestWithTracesOffTheCursorHooksKeepNothing(t *testing.T) {
	if _, err := declaredShells(cursorTarget{}, runtime.GOOS, channelTool); err != nil {
		t.Skipf("Cursor declares no tool shell on %s", runtime.GOOS)
	}
	hc := hookContext{cfgPath: "/c.toml", sessionsDir: "/sessions", turnEnd: true}
	search := cursorTestSearch(t, hc.cfgPath)
	base := map[string]any{"conversation_id": "conv-1", "generation_id": "gen-1", "workspace_roots": []string{"/w/proj"}, "cursor_version": "3.21.16"}
	with := func(extra map[string]any) []byte {
		p := map[string]any{}
		for k, v := range base {
			p[k] = v
		}
		for k, v := range extra {
			p[k] = v
		}
		return mustJSON(t, p)
	}

	session := func(t *testing.T, trace string) (fs *fakeHookFS, answers map[string]string) {
		t.Helper()
		fs, ops := newFakeHookOps(map[string]string{"JEVLIN_TRACE": trace})
		answers = map[string]string{}
		call := func(event string, payload []byte) {
			var out bytes.Buffer
			hookCursor(ops, hc, event, payload, &out, io.Discard)
			answers[event] = strings.TrimSpace(out.String())
		}
		call("sessionStart", with(nil))
		var start struct {
			Env map[string]string `json:"env"`
		}
		if err := json.Unmarshal([]byte(answers["sessionStart"]), &start); err != nil {
			t.Fatalf("sessionStart: %q", answers["sessionStart"])
		}
		ops.getenv = func(k string) string {
			if k == "JEVLIN_TRACE" {
				return trace
			}
			return start.Env[k]
		}
		call("beforeSubmitPrompt", with(map[string]any{"prompt": "the user's own question"}))
		call("afterAgentThought", with(map[string]any{"text": "the assistant's reasoning"}))
		call("afterAgentResponse", with(map[string]any{"text": "the assistant's answer"}))
		call("preToolUse", cursorPreToolUsePayload(t, search))
		call("beforeShellExecution", with(map[string]any{"command": search}))
		return fs, answers
	}

	onFS, on := session(t, "")
	if len(sessionsFiles(onFS)) == 0 || !strings.Contains(on["preToolUse"], "updated_input") {
		t.Fatalf("with tracing on nothing was kept or labeled, so the fixture proves nothing: files %v, preToolUse %q",
			keys(onFS.files), on["preToolUse"])
	}
	offFS, off := session(t, "off")
	if files := sessionsFiles(offFS); len(files) != 0 {
		t.Errorf("with traces off the hooks wrote %v", files)
	}
	if off["preToolUse"] != "" {
		t.Errorf("with traces off preToolUse answered %q", off["preToolUse"])
	}
	if off["beforeShellExecution"] != `{"permission":"allow"}` || off["beforeSubmitPrompt"] != `{"continue":true}` {
		t.Errorf("with traces off a permission answer changed: %q", off)
	}
}

// Codex's PreToolUse: with traces off our own rendered search gets the
// allow alone, as the fetch does, with the input as it came and no file.
func TestWithTracesOffCodexAllowsTheSearchAsIs(t *testing.T) {
	in := newCodexTestInstall(t, "/home/u/.jevlin/jevlin.toml")
	search := in.search(t)
	payload := withCommand(codexPayloadFixture(t, "codex-0.158.0-linux-PreToolUse-bash.json"), search)

	onFS, onOps := in.ops(nil)
	if out := runCodexOn(t, onOps, hcFor(in), "linux", "PreToolUse", payload); !strings.Contains(out, bridgeEnv) || len(onFS.files) == 0 {
		t.Fatalf("with tracing on the search was not bridged or nothing was kept, so the fixture proves nothing: %q, files %v", out, keys(onFS.files))
	}
	offFS, offOps := in.ops(map[string]string{"JEVLIN_TRACE": "off"})
	out := runCodexOn(t, offOps, hcFor(in), "linux", "PreToolUse", payload)
	var a codexAnswer
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatalf("with traces off the search got no answer: %q", out)
	}
	var input map[string]any
	_ = json.Unmarshal(a.Out["updatedInput"], &input)
	if input["command"] != search || strings.Contains(out, bridgeEnv) || !strings.Contains(out, `"allow"`) {
		t.Errorf("with traces off the answer is not an allow with the command as it came: %s", out)
	}
	if len(offFS.files) != 0 {
		t.Errorf("with traces off the hook wrote %v", keys(offFS.files))
	}
}
