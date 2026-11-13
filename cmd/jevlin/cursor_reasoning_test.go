package main

// The model's reasoning is never sent, by any host. Cursor's afterAgentThought
// is the one hook that hands this client reasoning, and it records nothing:
// a hooks file an earlier version installed still calls it, so it must
// succeed, but the trace's text comes from afterAgentResponse alone.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

const (
	cursorThought = "a private line of reasoning about the participant's files"
	cursorReply   = "Let me look that up."
)

// cursorTurn runs one Cursor turn through the real hooks: the prompt, the
// given text events, our search (allowed), the search's trace as
// `jevlin search` would build it, the served mark it would leave, and the
// stop. It returns the trace, the queued turn end and the fake filesystem's
// files, so a test can look for a text everywhere it could have gone.
func cursorTurn(t *testing.T, events ...[2]string) (trace []byte, turnEnd []byte, files map[string][]byte) {
	t.Helper()
	p := newLineageProbe(t, adoptRoot, nil)
	hc := hookContext{sessionsDir: adoptSessions, turnEnd: true}
	env := cursorSessionStart(t, p, hc, "conv", adoptRoot)
	ops := cursorSession(p, env)
	var queued []string
	ops.spawnTurnEnd = func(_, file string) error { queued = append(queued, file); return nil }

	cursorEvent(t, ops, hc, "beforeSubmitPrompt", "conv", adoptRoot, map[string]any{"prompt": "what did the docs say"})
	for _, ev := range events {
		if out := cursorEvent(t, ops, hc, ev[0], "conv", adoptRoot, map[string]any{"text": ev[1]}); out != "" {
			t.Fatalf("%s answered %q", ev[0], out)
		}
	}
	search := cursorTestSearch(t, "")
	if out := cursorEvent(t, ops, hc, "beforeShellExecution", "conv", adoptRoot, map[string]any{"command": search}); strings.TrimSpace(out) != `{"permission":"allow"}` {
		t.Fatalf("the search was not allowed: %q", out)
	}
	env2 := p.trace(env)
	if env2 == nil {
		t.Fatal("the search sent no trace")
	}
	trace, err := json.Marshal(env2)
	if err != nil {
		t.Fatal(err)
	}
	markTurnSearched(ops, config.Search{TurnEnd: true, SessionsDir: adoptSessions}, env2)
	// A thought after the search, before the turn ends, as Cursor sends one.
	cursorEvent(t, ops, hc, "afterAgentThought", "conv", adoptRoot, map[string]any{"text": cursorThought})
	cursorEvent(t, ops, hc, "stop", "conv", adoptRoot, map[string]any{"status": "completed"})
	if len(queued) != 1 {
		t.Fatalf("the turn queued %d turn end(s), want 1", len(queued))
	}
	return trace, p.fs.files[queued[0]], p.fs.files
}

// A thought's text reaches neither the search's trace nor the turn end, nor
// any file under the sessions directory. The control, a reply in the same
// place, reaches both, so the fixture is one that carries text.
func TestACursorThoughtReachesNoTraceAndNoTurnEnd(t *testing.T) {
	trace, turnEnd, _ := cursorTurn(t, [2]string{"afterAgentResponse", cursorReply})
	if !strings.Contains(string(trace), cursorReply) || !strings.Contains(string(turnEnd), cursorReply) {
		t.Fatalf("the control's reply did not reach the trace and the turn end:\ntrace %s\nturn end %s", trace, turnEnd)
	}
	for name, events := range map[string][][2]string{
		"a thought alone":               {{"afterAgentThought", cursorThought}},
		"a thought after a reply":       {{"afterAgentResponse", cursorReply}, {"afterAgentThought", cursorThought}},
		"a thought before a reply":      {{"afterAgentThought", cursorThought}, {"afterAgentResponse", cursorReply}},
		"two thoughts around the reply": {{"afterAgentThought", cursorThought}, {"afterAgentResponse", cursorReply}, {"afterAgentThought", cursorThought}},
	} {
		t.Run(name, func(t *testing.T) {
			trace, turnEnd, files := cursorTurn(t, events...)
			if strings.Contains(string(trace), cursorThought) || strings.Contains(string(trace), `"reasoning"`) {
				t.Errorf("the thought reached the search's trace: %s", trace)
			}
			if strings.Contains(string(turnEnd), cursorThought) {
				t.Errorf("the thought reached the turn end: %s", turnEnd)
			}
			for path, body := range files {
				if strings.Contains(string(body), cursorThought) {
					t.Errorf("the thought was stored at %s", path)
				}
			}
		})
	}
}

// A lineage file an earlier version's Cursor hook wrote can still hold the
// reasoning under its own role; a search that adopts it sends the rest of the
// file and not that. The control, the same file with the reply's role, sends
// the text.
func TestReasoningInALineageFileAnEarlierVersionWroteIsNotSent(t *testing.T) {
	for role, sent := range map[string]bool{"assistant": true, traceRoleReasoning: false} {
		t.Run(role, func(t *testing.T) {
			p := newLineageProbe(t, adoptRoot, nil)
			path := conversationLineagePath(adoptSessions, adoptRoot, "conv")
			if err := saveLineage(p.ops.hook, path, &lineageFile{Harness: "cursor", SessionID: traceHash("conv"), Window: "none",
				History: []traceHistory{{Role: role, Text: cursorThought}}}, p.now); err != nil {
				t.Fatal(err)
			}
			tr := p.trace(map[string]string{"JEVLIN_HARNESS": "cursor", lineageEnv: path, sessionEnv: traceHash("conv")})
			if tr == nil || tr.SessionID != traceHash("conv") {
				t.Fatalf("the search did not adopt the file: %+v", tr)
			}
			b, _ := json.Marshal(tr)
			if got := strings.Contains(string(b), cursorThought); got != sent {
				t.Errorf("role %q: text sent = %v, want %v: %s", role, got, sent, b)
			}
		})
	}
}
