package main

// Codex's turn end (codex_turn_end.go), from a session file and a Stop
// payload captured together on 2026-10-07: codex-cli 0.160.0 on macOS through
// `codex exec`, in a turn that ran `echo alpha`, this installation's search,
// and a subagent that ran a search of its own.
// testdata/codex/rollout-0.160.0-macos-search-subagent.jsonl is the session
// file as it stood when Stop fired, copied by a recording hook beside this
// client's; testdata/hook/codex-0.160.0-macos-Stop.json is that Stop's
// payload. Both are Codex's own bytes, key order included, with these
// changes: home and working directories are /Users/u placeholders, the time
// zone is Etc/UTC, session_meta's base instructions and account ids are
// replaced, the developer messages' text and world_state are replaced (they
// hold the installation's memory, skills and workspace state, not the turn),
// and the spawn-agent message is replaced.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The call id the loopback router received with this turn's search, in the
// run that wrote the fixtures: traceHash(session | tool_use_id), sent by
// PreToolUse's bridge.
const codexFixtureSearchCallID = "29c6aea0fd6743bbc7f456a29a606499"

type codexTurnRun struct {
	fs     *fakeHookFS
	ops    hookOps
	hc     hookContext
	stop   map[string]any
	mark   string
	queued []string
}

// newCodexTurnRun is an opted-in installation with the session file where
// the Stop payload names it and the served-search mark of its turn.
func newCodexTurnRun(t *testing.T) *codexTurnRun {
	t.Helper()
	stop := codexPayloadFixture(t, "codex-0.160.0-macos-Stop.json")
	rollout, err := os.ReadFile(filepath.Join("testdata", "codex", "rollout-0.160.0-macos-search-subagent.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	r := &codexTurnRun{stop: stop, hc: hookContext{cfgPath: "/Users/u/.jevlin/jevlin.toml", sessionsDir: "/sessions", turnEnd: true}}
	r.fs, r.ops = newFakeHookOps(nil)
	r.ops.spawnTurnEnd = func(_, path string) error { r.queued = append(r.queued, path); return nil }
	r.fs.files[stop["transcript_path"].(string)] = rollout
	r.mark = turnSearchedPath("/sessions", traceHash(stop["session_id"].(string)+"|"+stop["turn_id"].(string)))
	r.fs.files[r.mark] = nil
	return r
}

// run sends Stop and returns what was queued, decoded, with its bytes.
func (r *codexTurnRun) run(t *testing.T) (*turnEndRecord, []byte) {
	t.Helper()
	if out := runCodexOn(t, r.ops, r.hc, "darwin", "Stop", r.stop); out != "" {
		t.Errorf("Stop printed %q; Codex reads a Stop hook's output as a decision", out)
	}
	if len(r.fs.flushes) != 1 {
		t.Errorf("Stop started %d flushes, want one", len(r.fs.flushes))
	}
	if len(r.queued) == 0 {
		return nil, nil
	}
	if len(r.queued) > 1 {
		t.Fatalf("%d turn ends queued, want at most one", len(r.queued))
	}
	b := r.fs.files[r.queued[0]]
	var rec turnEndRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("queued turn end does not decode: %v", err)
	}
	return &rec, b
}

// The turn as Codex recorded it: what the user asked, what the assistant
// wrote while it worked, the shell command and the subagent calls by name
// and outcome, the search under the call id its own trace carried, the model,
// the turn's tokens, and the final answer once. Run as sent, and with no
// final message in the payload: then no closing text is trimmed from the
// steps, so nothing the reader took is hidden by the trim.
func TestCodexTurnEndCarriesTheTurnFromTheSessionFile(t *testing.T) {
	for name, finalMessage := range map[string]bool{"as sent": true, "no final message in the payload": false} {
		t.Run(name, func(t *testing.T) {
			r := newCodexTurnRun(t)
			if !finalMessage {
				delete(r.stop, "last_assistant_message")
			}
			checkCodexTurn(t, r, finalMessage)
		})
	}
}

func checkCodexTurn(t *testing.T, r *codexTurnRun, finalMessage bool) {
	last, _ := r.stop["last_assistant_message"].(string)
	rec, raw := r.run(t)
	if rec == nil {
		t.Fatal("no turn end was queued for an opted-in turn that searched")
	}
	session, turn := r.stop["session_id"].(string), r.stop["turn_id"].(string)
	if rec.SessionID != traceHash(session) || rec.TurnID != traceHash(session+"|"+turn) || rec.Harness != "codex" || rec.Status != turnCompleted {
		t.Errorf("record ids: %+v", rec)
	}
	if final, _, _ := prepareFinalText(last); rec.FinalText != final || (finalMessage && final == "") {
		t.Errorf("final text %q, want the payload's message prepared: %q", rec.FinalText, final)
	}
	if !strings.HasPrefix(rec.UserText, "This is a throwaway test of a search tool") || strings.Contains(rec.UserText, "environment_context") {
		t.Errorf("user text is not the prompt alone: %.120q", rec.UserText)
	}
	if rec.Model != "gpt-6.1-sol" {
		t.Errorf("model %q", rec.Model)
	}
	if rec.Usage == nil || rec.Usage.InputTokens != 106500 || rec.Usage.OutputTokens != 464 {
		t.Errorf("usage %+v, want the turn's own totals 106500 in, 464 out", rec.Usage)
	}
	var shape []string
	for _, st := range rec.Steps {
		switch st.Kind {
		case "assistant":
			shape = append(shape, "assistant")
		case "search":
			shape = append(shape, "search")
			if st.CallID != codexFixtureSearchCallID {
				t.Errorf("search call id %q, want the one its trace carried, %s", st.CallID, codexFixtureSearchCallID)
			}
		default:
			shape = append(shape, st.Kind+":"+st.Name)
		}
		// Codex records an outcome for the commands and for the wait, as
		// an exit code and as an item's status; the spawn call returns no
		// status, and its outcome is left unknown rather than assumed.
		switch {
		case st.Kind == "assistant":
		case st.Name == "spawn_agent":
			if st.OK != nil {
				t.Errorf("spawn_agent has outcome %v; Codex recorded none", *st.OK)
			}
		case st.OK == nil || !*st.OK:
			t.Errorf("step %s %s: outcome %v, want succeeded", st.Kind, st.Name, st.OK)
		}
	}
	if got, want := strings.Join(shape, " "), "assistant tool:Bash search tool:spawn_agent tool:wait_agent"; got != want {
		t.Errorf("steps %q, want %q", got, want)
	}
	// A shell command gets no duration: the times Codex gives its item are
	// not the command's. The subagent calls get theirs, from the calls' own
	// records.
	for _, st := range rec.Steps {
		if (st.Name == "Bash" || st.Kind == "search") && st.Ms != 0 {
			t.Errorf("%s %s carries %d ms, a time Codex did not record for it", st.Kind, st.Name, st.Ms)
		}
		if (st.Name == "spawn_agent" || st.Name == "wait_agent") && st.Ms <= 0 {
			t.Errorf("%s carries no duration; its call and output records give one", st.Name)
		}
	}
	// What a tool was given and what it returned, the subagent's own answer,
	// and the developer instructions are in the file, and none of it is
	// sent.
	for _, never := range []string{
		"JEVLIN_TRACE_BRIDGE", "/bin/zsh", "tools.exec_command", "Script completed", "unified_exec",
		"FINAL_ANSWER", "Task name:", "spawned agent's task", "developer instructions", "base instructions",
	} {
		if bytes.Contains(raw, []byte(never)) {
			t.Errorf("the turn end carries %q", never)
		}
	}
	if _, left := r.fs.files[r.mark]; left {
		t.Error("the served-search mark was left: the turn would end twice")
	}
}

// Nothing is sent without the opt-in, for a turn no search was served in, for
// a Stop another stop hook is continuing, for a Stop that names an agent, or
// with tracing switched off. Each still starts its flush and prints nothing.
func TestCodexTurnEndSendsNothingItShouldNot(t *testing.T) {
	for name, change := range map[string]func(r *codexTurnRun){
		"not opted in":          func(r *codexTurnRun) { r.hc.turnEnd = false },
		"no search served":      func(r *codexTurnRun) { delete(r.fs.files, r.mark) },
		"a stop hook continues": func(r *codexTurnRun) { r.stop["stop_hook_active"] = true },
		// Codex ends a subagent with SubagentStop, which is not installed; a
		// Stop that names an agent is refused all the same.
		"a subagent's Stop": func(r *codexTurnRun) {
			r.stop["agent_id"], r.stop["agent_type"] = "01a112eb-5237-7970-b008-e3ecaa26aa4a", "default"
		},
		"tracing switched off": func(r *codexTurnRun) {
			r.ops.getenv = func(k string) string { return map[string]string{"JEVLIN_TRACE": "off"}[k] }
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newCodexTurnRun(t)
			change(r)
			if rec, _ := r.run(t); rec != nil {
				t.Errorf("a turn end was queued: %+v", rec)
			}
			for p := range r.fs.files {
				if strings.HasSuffix(p, turnEndSuffix) {
					t.Errorf("a turn end file was written: %s", p)
				}
			}
		})
	}
}

// A turn whose start is not in the part of the session file read is sent
// without its conversation, as Claude Code's is: the answer and how it
// ended, never a guess at where it began. So is a turn whose file is gone.
func TestCodexTurnEndWithoutTheTurnsStartSendsTheAnswerOnly(t *testing.T) {
	for name, change := range map[string]func(r *codexTurnRun){
		"no task_started": func(r *codexTurnRun) {
			path := r.stop["transcript_path"].(string)
			var kept [][]byte
			for _, line := range bytes.Split(r.fs.files[path], []byte("\n")) {
				if !bytes.Contains(line, []byte(`"type":"task_started"`)) {
					kept = append(kept, line)
				}
			}
			r.fs.files[path] = bytes.Join(kept, []byte("\n"))
		},
		"no file": func(r *codexTurnRun) { delete(r.fs.files, r.stop["transcript_path"].(string)) },
	} {
		t.Run(name, func(t *testing.T) {
			r := newCodexTurnRun(t)
			change(r)
			rec, _ := r.run(t)
			if rec == nil {
				t.Fatal("no turn end: the answer and how the turn ended are still sent")
			}
			if rec.FinalText == "" || rec.UserText != "" || len(rec.Steps) != 0 || rec.Usage != nil {
				t.Errorf("record carries a conversation it could not read whole: %+v", rec)
			}
		})
	}
}
