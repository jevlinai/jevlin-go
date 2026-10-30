package main

import (
	"strings"
	"testing"
)

// JEVLIN_TRACE=off turns traces off entirely: the lineage hook writes no
// bridge onto the command and no lineage file, instead of writing both for
// search to drop.
func TestTheLineageHookWritesNothingWhenTraceIsOff(t *testing.T) {
	for _, value := range []string{"off", "OFF", " off "} {
		t.Run(strings.TrimSpace(value), func(t *testing.T) {
			fs, ops := newFakeHookOps(map[string]string{"JEVLIN_TRACE": value})
			fs.files["/t/s.jsonl"] = readHookFixture(t, "claude_code_skill_ismeta.jsonl")
			hc := hookContext{sessionsDir: "/sess"}
			before := len(fs.files)
			out, errOut := runHook(t, ops, hc, "lineage", map[string]any{
				"session_id": "s", "prompt_id": "p", "tool_use_id": "toolu_bash1", "cwd": "/w/proj",
				"transcript_path": "/t/s.jsonl", "tool_input": map[string]any{"command": `jevlin search -format model "q"`},
			})
			if out != "" || errOut != "" {
				t.Errorf("the hook answered with the trace off:\nstdout %q\nstderr %q", out, errOut)
			}
			if len(fs.files) != before {
				t.Errorf("the hook wrote a file with the trace off: %v", keys(fs.files))
			}
		})
	}
	// The control: with the variable unset the same call rewrites the
	// command and writes the lineage file.
	fs, ops := newFakeHookOps(nil)
	fs.files["/t/s.jsonl"] = readHookFixture(t, "claude_code_skill_ismeta.jsonl")
	before := len(fs.files)
	out, _ := runHook(t, ops, hookContext{sessionsDir: "/sess"}, "lineage", map[string]any{
		"session_id": "s", "prompt_id": "p", "tool_use_id": "toolu_bash1", "cwd": "/w/proj",
		"transcript_path": "/t/s.jsonl", "tool_input": map[string]any{"command": `jevlin search -format model "q"`},
	})
	if !strings.Contains(out, bridgeEnv) || len(fs.files) == before {
		t.Fatalf("the control did not trace: stdout %q, files %v", out, keys(fs.files))
	}
}
