package main

import (
	"strings"
	"testing"
)

// JEVLIN_TRACE=off and [search] trace = false each turn traces off entirely:
// the lineage hook writes no bridge onto the command and no lineage file,
// instead of writing both for search to drop.
func TestTheLineageHookWritesNothingWhenTraceIsOff(t *testing.T) {
	for _, sw := range append(envTraceSpellings("off", "OFF", " off ", "0", "false"), traceOffInConfig) {
		t.Run(sw.name, func(t *testing.T) {
			fs, ops := newFakeHookOps(sw.env)
			fs.files["/t/s.jsonl"] = readHookFixture(t, "claude_code_skill_ismeta.jsonl")
			hc := hookContext{sessionsDir: "/sess", traceOffInConfig: sw.configOff}
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

// Hermes' hook only adds a bridge; with traces off it adds none, in every
// spelling of the variable and by the config, after a control run that does.
func TestTheHermesHookWritesNoBridgeWhenTraceIsOff(t *testing.T) {
	payload := map[string]any{
		"hook_event_name": "pre_tool_call", "tool_name": "terminal",
		"tool_input": map[string]any{"command": `jevlin search -format model "q"`, "timeout": 30},
		"session_id": "sess-1", "cwd": "/w", "extra": map[string]any{"tool_call_id": "c1", "turn_id": "t1"},
	}
	_, on := newFakeHookOps(nil)
	if out, _ := runHook(t, on, hookContext{}, "hermes pre_tool_call", payload); !strings.Contains(out, bridgeEnv) {
		t.Fatalf("control: no bridge with traces on: %q", out)
	}
	for _, sw := range append(envTraceSpellings("off", "0", "false", " OFF "), traceOffInConfig) {
		_, ops := newFakeHookOps(sw.env)
		if out, _ := runHook(t, ops, hookContext{traceOffInConfig: sw.configOff}, "hermes pre_tool_call", payload); out != "" {
			t.Errorf("%s: the hook answered %q", sw.name, out)
		}
	}
}
