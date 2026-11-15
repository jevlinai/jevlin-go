package main

// Cursor's reply goes with a search only in the turn that wrote it.
//
// afterAgentResponse keeps the reply in the conversation's lineage file,
// stamped with its turn, and nothing clears it when the next turn starts. A
// search builds its trace from that file, so the first search of a new turn
// sent the previous turn's reply as the text that led to it. Everything here
// runs the real hooks and the real searchTrace over one in-memory machine.

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	cursorEarlierReply = "Here is the summary of the release notes you asked for."
	cursorCurrentReply = "Let me look up the changelog."
)

// cursorGeneration runs the given events of one Cursor generation, in order,
// each with its text, and answers the trace a search in it sent, or nil when
// the generation made no search.
func cursorGeneration(t *testing.T, p *lineageProbe, ops hookOps, hc hookContext, env map[string]string, generation string, events ...[2]string) *traceEnvelope {
	t.Helper()
	var trace *traceEnvelope
	for _, ev := range events {
		extra := map[string]any{"generation_id": generation}
		switch ev[0] {
		case "beforeSubmitPrompt":
			extra["prompt"] = ev[1]
		case "afterAgentResponse":
			extra["text"] = ev[1]
		case "stop":
			extra["status"] = "completed"
		case "search":
			if out := cursorEvent(t, ops, hc, "beforeShellExecution", "conv", adoptRoot, map[string]any{"generation_id": generation, "command": cursorTestSearch(t, "")}); strings.TrimSpace(out) != `{"permission":"allow"}` {
				t.Fatalf("the search was not allowed: %q", out)
			}
			if trace = p.trace(env); trace == nil {
				t.Fatal("the search sent no trace")
			}
			continue
		}
		cursorEvent(t, ops, hc, ev[0], "conv", adoptRoot, extra)
	}
	return trace
}

func TestACursorReplyIsSentOnlyWithASearchOfItsOwnTurn(t *testing.T) {
	for _, turnEnd := range []bool{false, true} {
		name := "turn end off"
		if turnEnd {
			name = "turn end on"
		}
		t.Run(name, func(t *testing.T) {
			setup := func(t *testing.T) (*lineageProbe, hookOps, hookContext, map[string]string) {
				p := newLineageProbe(t, adoptRoot, nil)
				hc := hookContext{sessionsDir: adoptSessions, turnEnd: turnEnd}
				env := cursorSessionStart(t, p, hc, "conv", adoptRoot)
				ops := cursorSession(p, env)
				ops.spawnTurnEnd = func(_, _ string) error { return nil }
				return p, ops, hc, env
			}

			// The reported case: a reply in generation A, then a new
			// prompt and a search in generation B with no reply before it.
			t.Run("a later turn", func(t *testing.T) {
				p, ops, hc, env := setup(t)
				cursorGeneration(t, p, ops, hc, env, "gen-a",
					[2]string{"beforeSubmitPrompt", "summarize the release notes"},
					[2]string{"afterAgentResponse", cursorEarlierReply},
					[2]string{"stop", ""})
				tr := cursorGeneration(t, p, ops, hc, env, "gen-b",
					[2]string{"beforeSubmitPrompt", "now check the changelog"},
					[2]string{"search", ""})
				b, _ := json.Marshal(tr)
				if len(tr.History) != 0 || strings.Contains(string(b), cursorEarlierReply) {
					t.Errorf("generation B's search carried generation A's reply: %s", b)
				}
				if tr.SessionID != traceHash("conv") || tr.TurnID != traceHash("conv|gen-b") {
					t.Errorf("the search was not traced under its own session and turn: %s", b)
				}
			})

			// The control: a reply in the same generation, before its
			// search, is the text that led to it, and is sent.
			t.Run("the same turn", func(t *testing.T) {
				p, ops, hc, env := setup(t)
				cursorGeneration(t, p, ops, hc, env, "gen-a",
					[2]string{"beforeSubmitPrompt", "summarize the release notes"},
					[2]string{"afterAgentResponse", cursorEarlierReply},
					[2]string{"stop", ""})
				tr := cursorGeneration(t, p, ops, hc, env, "gen-b",
					[2]string{"beforeSubmitPrompt", "now check the changelog"},
					[2]string{"afterAgentResponse", cursorCurrentReply},
					[2]string{"search", ""})
				b, _ := json.Marshal(tr)
				if len(tr.History) != 1 || tr.History[0].Text != cursorCurrentReply {
					t.Errorf("generation B's own reply did not reach its search's trace: %s", b)
				}
				if strings.Contains(string(b), cursorEarlierReply) {
					t.Errorf("generation A's reply reached generation B's search: %s", b)
				}
			})
		})
	}
}

// A reply Cursor sent with no generation cannot be placed in a turn, and is
// sent under none.
func TestACursorReplyWithNoTurnIsNotSent(t *testing.T) {
	p := newLineageProbe(t, adoptRoot, nil)
	hc := hookContext{sessionsDir: adoptSessions}
	env := cursorSessionStart(t, p, hc, "conv", adoptRoot)
	ops := cursorSession(p, env)
	tr := cursorGeneration(t, p, ops, hc, env, "",
		[2]string{"afterAgentResponse", cursorCurrentReply},
		[2]string{"search", ""})
	if b, _ := json.Marshal(tr); len(tr.History) != 0 {
		t.Errorf("a reply with no turn reached the search's trace: %s", b)
	}
}
