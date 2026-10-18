package main

// A panic while preparing the trace costs the trace, never the search (hard
// invariant 1, #48). traceScrubTestHook stands in for a scrubber bug: it runs
// on every text prepareTraceText is about to scrub, and here it panics. Each
// path that prepares a trace is driven through its real entry point,
// searchMain and hookMain, and first without the panic, so the test shows the
// path reaches the scrubber at all.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// panicInTheScrubber makes every scrub panic until the test ends, and counts
// how many were attempted.
func panicInTheScrubber(t *testing.T) *int {
	t.Helper()
	n := 0
	traceScrubTestHook = func(string) {
		n++
		panic("a scrubber bug")
	}
	t.Cleanup(func() { traceScrubTestHook = nil })
	return &n
}

func TestASearchWhoseTracePanicsIsSentWithoutOne(t *testing.T) {
	for _, c := range []struct {
		name string
		env  func(t *testing.T, p *channelProbe) map[string]string
	}{
		{"the bridge's history", func(t *testing.T, p *channelProbe) map[string]string {
			return map[string]string{"JEVLIN_API_KEY": "k", bridgeEnv: someoneElsesBridge(t, "claude-code")}
		}},
		{"the lineage file's history", func(t *testing.T, p *channelProbe) map[string]string {
			return map[string]string{"JEVLIN_API_KEY": "k", "JEVLIN_HARNESS": "cursor", lineageEnv: p.lineage}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Without the panic the search carries a trace, so the path
			// reaches the scrubber.
			p := newChannelProbe(t)
			if code, _, stderr := runSearch(t, p.h, c.env(t, p), "-config", p.cfg, "q"); code != exitOK {
				t.Fatalf("fixture: exit %d: %s", code, stderr)
			}
			p.sentTrace(t)

			p = newChannelProbe(t)
			env := c.env(t, p)
			attempts := panicInTheScrubber(t)
			code, out, stderr := runSearch(t, p.h, env, "-config", p.cfg, "q")
			if *attempts == 0 {
				t.Fatal("fixture: the search never reached the scrubber")
			}
			if code != exitOK || out == "" {
				t.Fatalf("a panic in the trace failed the search: exit %d, stdout %q, stderr %q", code, out, stderr)
			}
			p.fr.mu.Lock()
			requests := len(p.fr.reqs)
			p.fr.mu.Unlock()
			if requests != 1 {
				t.Fatalf("the router got %d requests, want the one search", requests)
			}
			_, sent := p.fr.last(t)
			var body map[string]any
			if err := json.Unmarshal(sent, &body); err != nil {
				t.Fatalf("the router received something that is not JSON: %s", sent)
			}
			if _, ok := body["trace"]; ok {
				t.Errorf("a trace rode although preparing it panicked: %s", sent)
			}
			if !strings.Contains(stderr, "without a trace") {
				t.Errorf("the dropped trace is not said on stderr: %q", stderr)
			}
			if strings.Contains(out, "trace") || strings.Contains(stderr, "a scrubber bug") {
				t.Errorf("the panic reached what the model reads, or its value was repeated:\nstdout %q\nstderr %q", out, stderr)
			}
		})
	}
}

func TestAHookWhoseTracePanicsEmitsNothingAndExitsZero(t *testing.T) {
	run := func() (int, string, string) {
		fs, ops := newFakeHookOps(nil)
		fs.files["/t/s.jsonl"] = []byte(strings.Join([]string{
			`{"type":"user","message":{"role":"user","content":"new turn"}}`,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Let me look that up."},{"type":"tool_use","id":"toolu_1","name":"Bash"}]}}`,
		}, "\n"))
		payload, _ := json.Marshal(map[string]any{
			"session_id": "sess-1", "prompt_id": "p-1", "tool_use_id": "toolu_1", "tool_name": "Bash",
			"cwd": "/home/u/project", "transcript_path": "/t/s.jsonl",
			"tool_input": map[string]any{"command": `jevlin search -format model "q"`},
		})
		var out, errOut bytes.Buffer
		code := hookMain(ops, []string{"lineage"}, bytes.NewReader(payload), &out, &errOut)
		return code, out.String(), errOut.String()
	}
	// Without the panic the hook answers with the bridge: the path reaches
	// the scrubber, and the answer hookMain holds back reaches stdout.
	if code, out, _ := run(); code != exitOK || !strings.Contains(out, bridgeEnv+"=") {
		t.Fatalf("fixture: exit %d, %q", code, out)
	}

	attempts := panicInTheScrubber(t)
	code, out, errOut := run()
	if *attempts == 0 {
		t.Fatal("fixture: the hook never reached the scrubber")
	}
	if code != exitOK {
		t.Fatalf("a panic in the trace exited %d; Claude Code reads exit 2 from PreToolUse as a block", code)
	}
	if out != "" || errOut != "" {
		t.Errorf("the hook emitted something after a panic:\nstdout %q\nstderr %q", out, errOut)
	}
}
