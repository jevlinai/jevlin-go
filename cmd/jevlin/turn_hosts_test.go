package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// What a JavaScript host pipes to `hook turn` is another program's input:
// it is rebuilt field by field, scrubbed and capped, and joined to the
// turn's searches by the same hashes the host's search envelope used.
func TestHostTurn(t *testing.T) {
	h := newTurnEndHarness(nil, true)
	h.served(traceHash("ses_1|msg_user"))
	payload := map[string]any{
		"session": "ses_1", "turn": "msg_user", "status": "completed",
		"user_text":  "find it; my token is ghp_" + strings.Repeat("a", 30),
		"final_text": "Here it is.", "model": "claude-opus-5-5",
		"usage": map[string]any{"input_tokens": 900, "output_tokens": 40},
		"steps": []map[string]any{
			{"kind": "assistant", "text": "Looking."},
			// Fields a plugin has no business sending are not read.
			{"kind": "tool", "name": "read", "ms": 12, "ok": true, "input": "/Users/someone/secret.txt", "output": "FILE CONTENTS", "text": "SMUGGLED"},
			{"kind": "search", "call": "call_9", "ms": 5200, "ok": true, "command": "jevlin search private-query"},
			{"kind": "tool", "name": "a name with spaces", "ok": false},
			{"kind": "unknown", "text": "IGNORED KIND"},
		},
	}
	runHook(t, h.ops, h.hc, "turn opencode", payload)
	rec := h.record(t)
	raw := string(h.fs.files[h.queued[0]])
	for _, leak := range []string{"secret.txt", "FILE CONTENTS", "SMUGGLED", "private-query", "IGNORED KIND", "ghp_aaaa", "a name with spaces", "ses_1", "msg_user", "call_9"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the queued record contains %q", leak)
		}
	}
	if rec.Harness != "opencode" || rec.SessionID != traceHash("ses_1") || rec.TurnID != traceHash("ses_1|msg_user") || rec.Status != turnCompleted {
		t.Errorf("ids = %+v", rec)
	}
	if !strings.Contains(rec.UserText, "[REDACTED]") || rec.FinalText != "Here it is." || rec.Model != "claude-opus-5-5" ||
		rec.Usage == nil || rec.Usage.InputTokens != 900 || rec.Usage.OutputTokens != 40 {
		t.Errorf("record = %+v", rec)
	}
	if len(rec.Steps) != 4 || rec.Steps[0].Text != "Looking." || rec.Steps[1].Name != "read" || rec.Steps[1].Ms != 12 ||
		rec.Steps[2].Kind != "search" || rec.Steps[2].CallID != traceHash("ses_1|call_9") || rec.Steps[3].Name != "tool" || *rec.Steps[3].OK {
		t.Errorf("steps = %+v", rec.Steps)
	}

	for name, tc := range map[string]struct {
		enabled, served bool
		over            map[string]any
	}{
		"not opted in":      {false, true, nil},
		"no search served":  {true, false, nil},
		"no session":        {true, true, map[string]any{"session": ""}},
		"an unknown status": {true, true, map[string]any{"status": "done"}},
		"not json at all":   {true, true, map[string]any{"__raw": "{"}},
	} {
		h := newTurnEndHarness(nil, tc.enabled)
		if tc.served {
			h.served(traceHash("ses_1|msg_user"))
		}
		p := map[string]any{}
		for k, v := range payload {
			p[k] = v
		}
		for k, v := range tc.over {
			p[k] = v
		}
		var body any = p
		if raw, ok := p["__raw"]; ok {
			body = raw
		}
		runHook(t, h.ops, h.hc, "turn opencode", body)
		if len(h.queued) != 0 {
			t.Errorf("%s: queued %v", name, h.queued)
		}
	}

	// An interrupted turn is reported without an answer.
	h = newTurnEndHarness(nil, true)
	h.served(traceHash("ses_1|msg_user"))
	payload["status"] = "interrupted"
	runHook(t, h.ops, h.hc, "turn opencode", payload)
	if rec := h.record(t); rec.Status != turnInterrupted || rec.FinalText != "" || rec.UserText == "" {
		t.Errorf("interrupted = %+v", rec)
	}
}

// Cursor's turn arrives in pieces: the prompt at beforeSubmitPrompt, each
// search at beforeShellExecution, the reply at afterAgentResponse, the model
// and tokens at stop.
func TestCursorTurnConversation(t *testing.T) {
	path := conversationLineagePath("/home/sessions", "/work", "conv")
	turn := traceHash("conv|gen1")
	ev := func(h *turnEndHarness, name string, extra map[string]any) string {
		t.Helper()
		p := map[string]any{"conversation_id": "conv", "generation_id": "gen1", "cwd": "/work", "cursor_version": "3.20.21"}
		for k, v := range extra {
			p[k] = v
		}
		out, _ := runHook(t, h.ops, h.hc, "cursor "+name, p)
		return out
	}

	h := newTurnEndHarness(nil, true)
	secret := "sk-" + strings.Repeat("B", 40)
	if out := ev(h, "beforeSubmitPrompt", map[string]any{"prompt": "what seats does it have? key " + secret}); strings.TrimSpace(out) != `{"continue":true}` {
		t.Errorf("beforeSubmitPrompt answered %q", out)
	}
	l, ok := loadLineage(h.ops, path)
	if !ok || l.UserTurnID != turn || !strings.Contains(l.UserText, "[REDACTED]") || strings.Contains(string(h.fs.files[path]), secret) {
		t.Fatalf("the prompt as kept: %+v", l)
	}
	// Two searches in the turn, as beforeShellExecution records them. The
	// hook recognizes a command from a real installation; here the lineage
	// file is given what that leaves.
	if err := updateLineage(h.ops, path, time.Now(), func(l *lineageFile) {
		l.TurnID, l.SearchesTurnID, l.TurnSearches = turn, turn, []string{"call-a", "call-b"}
	}); err != nil {
		t.Fatal(err)
	}
	h.served(turn)
	ev(h, "afterAgentResponse", map[string]any{"text": "About 22,000."})
	ev(h, "stop", map[string]any{"status": "completed", "model": "claude-opus-5-5",
		"input_tokens": 100, "cache_read_tokens": 900, "cache_write_tokens": 0, "output_tokens": 30})
	rec := h.record(t)
	if rec.FinalText != "About 22,000." || !strings.HasPrefix(rec.UserText, "what seats does it have?") || rec.Model != "claude-opus-5-5" ||
		rec.Usage == nil || rec.Usage.InputTokens != 1000 || rec.Usage.OutputTokens != 30 {
		t.Errorf("record = %+v", rec)
	}
	if len(rec.Steps) != 2 || rec.Steps[0].Kind != "search" || rec.Steps[0].CallID != "call-a" || rec.Steps[1].CallID != "call-b" {
		t.Errorf("steps = %+v", rec.Steps)
	}
	// The turn is over: nothing of it stays in the file.
	if l, _ := loadLineage(h.ops, path); l.UserText != "" || l.UserTurnID != "" || len(l.TurnSearches) != 0 {
		t.Errorf("after stop the file still holds the turn: %+v", l)
	}

	// Not opted in: the hook still answers, and keeps nothing.
	off := newTurnEndHarness(nil, false)
	if out := ev(off, "beforeSubmitPrompt", map[string]any{"prompt": "a private question"}); strings.TrimSpace(out) != `{"continue":true}` {
		t.Errorf("beforeSubmitPrompt, not opted in, answered %q", out)
	}
	for p, b := range off.fs.files {
		if strings.Contains(string(b), "private question") {
			t.Errorf("a prompt was written to %s without opting in", p)
		}
	}

	// A prompt kept for one turn is not sent under the next.
	stale := newTurnEndHarness(nil, true)
	ev(stale, "beforeSubmitPrompt", map[string]any{"prompt": "the previous question", "generation_id": "gen0"})
	stale.served(turn)
	ev(stale, "stop", map[string]any{"status": "completed"})
	if rec := stale.record(t); rec.UserText != "" {
		t.Errorf("another turn's prompt was sent: %q", rec.UserText)
	}
}

// The opencode plugin, run under node with a stand-in for opencode's client
// and a stand-in for the binary that writes what it was piped to a file.
func TestOpencodePluginReportsTheTurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in binary is a POSIX shell script")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to verify the embedded opencode plugin")
	}
	dir := t.TempDir()
	got := filepath.Join(dir, "got.json")
	argv := filepath.Join(dir, "argv.txt")
	bin := filepath.Join(dir, "jevlin")
	count := filepath.Join(dir, "count.txt")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+argv+"'\ncat > '"+got+".tmp' && mv '"+got+".tmp' '"+got+"'\necho x >> '"+count+"'\n"), 0o700); err != nil { // #nosec G306 -- a test stand-in that must be executable
		t.Fatal(err)
	}
	script := `
 const fs = await import('node:fs');
 const input = JSON.parse(fs.readFileSync(0,'utf8'));
 const plugin = await import('data:text/javascript;base64,'+Buffer.from(input.plugin).toString('base64'));
 const messages = [
  {info:{id:'m_old',role:'user'}, parts:[{type:'text',text:'an earlier question'}]},
  {info:{id:'m_old_a',role:'assistant'}, parts:[{type:'text',text:'an earlier answer'}]},
  {info:{id:'m_user',role:'user'}, parts:[{type:'text',text:'who designed it?'},{type:'text',text:'SYNTHETIC',synthetic:true}]},
  {info:{id:'m_a1',role:'assistant',modelID:'claude-opus-5-5',tokens:{input:100,output:20,cache:{read:900,write:0}}}, parts:[
    {type:'text',text:'Let me look.'},
    {type:'tool',tool:'read',callID:'c_read',state:{status:'completed',input:{filePath:'/Users/someone/secret.txt'},output:'FILE CONTENTS',time:{start:1000,end:1012}}},
    {type:'tool',tool:'bash',callID:'c_search',state:{status:'completed',input:{command:'JEVLIN_TRACE_BRIDGE=abc jevlin search stadium'},output:'RESULTS TEXT',time:{start:2000,end:7200}}},
    {type:'tool',tool:'bash',callID:'c_rm',state:{status:'error',input:{command:'rm -rf build'},error:'denied',time:{start:8000,end:8040}}},
  ]},
  {info:{id:'m_a2',role:'assistant',modelID:'claude-opus-5-5',tokens:{input:50,output:10,cache:{read:0,write:0}}}, parts:[{type:'text',text:'Populous designed it.'}]},
 ];
 const client = {session:{ messages: async()=>({data:messages}), get: async({path})=>{
  if (path.id==='ses_unknown') throw new Error('unreachable');
  return {data:{id:path.id, ...(path.id==='ses_child2'?{parentID:'ses_1'}:{})}};
 } }};
 const hooks = await plugin.JevlinLineage({client});
 const result = {};
 // idle before any search: nothing is reported
 await hooks.event({event:{type:'session.idle',properties:{sessionID:'ses_1'}}});
 await new Promise(r=>setTimeout(r,300));
 result.before = fs.existsSync(input.got);
 const output={args:{command:'jevlin search stadium'}};
 await hooks['tool.execute.before']({tool:'bash',sessionID:'ses_1',callID:'c_search'},output);
 result.env = JSON.parse(Buffer.from(output.args.command.split(' ')[0].split('=')[1],'base64url').toString('utf8'));
 await hooks.event({event:{type:'session.idle',properties:{sessionID:'ses_1'}}});
 for (let i=0;i<50 && !fs.existsSync(input.got);i++) await new Promise(r=>setTimeout(r,100));
 result.raw = fs.readFileSync(input.got,'utf8');
 result.turn = JSON.parse(result.raw);
 result.argv = fs.readFileSync(input.argv,'utf8').trim().split('\n');
 // idle again, said the newer way: the same turn is not reported twice
 fs.unlinkSync(input.got);
 await hooks.event({event:{type:'session.status',properties:{sessionID:'ses_1',status:{type:'idle'}}}});
 await new Promise(r=>setTimeout(r,400));
 result.again = fs.existsSync(input.got);
 // A subagent's session reports nothing: one whose creation the plugin saw,
 // one whose parent it had to ask for, and one whose parent it could not learn.
 await hooks.event({event:{type:'session.created',properties:{info:{id:'ses_child',parentID:'ses_1'}}}});
 result.children = {};
 for (const sid of ['ses_child','ses_child2','ses_unknown']) {
  if (fs.existsSync(input.got)) fs.unlinkSync(input.got);
  await hooks['tool.execute.before']({tool:'bash',sessionID:sid,callID:'c_'+sid},{args:{command:'jevlin search stadium'}});
  await hooks.event({event:{type:'session.idle',properties:{sessionID:sid}}});
  await new Promise(r=>setTimeout(r,400));
  result.children[sid] = fs.existsSync(input.got);
 }
 // Both idle events at once, the second arriving while the first still waits
 // on its lookups: the turn is reported once.
 const reports = () => fs.existsSync(input.count) ? fs.readFileSync(input.count,'utf8').trim().split('\n').length : 0;
 const before = reports();
 await hooks['tool.execute.before']({tool:'bash',sessionID:'ses_2',callID:'c_ses_2'},{args:{command:'jevlin search stadium'}});
 await Promise.all([
  hooks.event({event:{type:'session.idle',properties:{sessionID:'ses_2'}}}),
  hooks.event({event:{type:'session.status',properties:{sessionID:'ses_2',status:{type:'idle'}}}}),
 ]);
 await new Promise(r=>setTimeout(r,500));
 result.concurrent = reports() - before;
 process.stdout.write(JSON.stringify(result));`
	in, _ := json.Marshal(map[string]any{"plugin": renderAgentScriptFor(opencodePluginJS, shellPOSIX, "/etc/jevlin.toml", bin), "got": got, "argv": argv, "count": count})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", script) // #nosec G204 -- fixed test script and local Node runtime; synthetic input on stdin
	cmd.Stdin = strings.NewReader(string(in))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("plugin: %v %s", err, out)
	}
	var res struct {
		Before, Again bool
		Children      map[string]bool
		Concurrent    int
		Env           traceEnvelope
		Raw           string
		Argv          []string
		Turn          hostTurn
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if res.Before || res.Again {
		t.Errorf("reported before a search (%v) or twice (%v)", res.Before, res.Again)
	}
	if len(res.Children) != 3 {
		t.Fatalf("the subagent cases did not all run: %v", res.Children)
	}
	if res.Concurrent != 1 {
		t.Errorf("two idle events at once reported the turn %d times, want once", res.Concurrent)
	}
	for sid, reported := range res.Children {
		if reported {
			t.Errorf("a subagent's turn was reported (%s)", sid)
		}
	}
	// The search now carries a turn id, and the report names the same turn.
	if res.Env.TurnID != traceHash("ses_1|m_user") || res.Turn.Session != "ses_1" || res.Turn.Turn != "m_user" {
		t.Errorf("turn ids: envelope %q, report %q/%q", res.Env.TurnID, res.Turn.Session, res.Turn.Turn)
	}
	if strings.Join(res.Argv, " ") != "hook -config /etc/jevlin.toml turn opencode" {
		t.Errorf("argv = %q", res.Argv)
	}
	// What was actually piped, byte for byte: no tool's input or output, no
	// command, nothing synthetic, nothing from an earlier turn.
	if res.Raw == "" {
		t.Fatal("nothing was piped")
	}
	for _, leak := range []string{"secret.txt", "FILE CONTENTS", "RESULTS TEXT", "rm -rf", "denied", "SYNTHETIC", "an earlier", "search stadium", "JEVLIN_TRACE_BRIDGE"} {
		if strings.Contains(res.Raw, leak) {
			t.Errorf("the plugin piped %q", leak)
		}
	}
	tr := res.Turn
	if tr.UserText != "who designed it?" || tr.FinalText != "Populous designed it." || tr.Status != "completed" || tr.Model != "claude-opus-5-5" ||
		tr.Usage == nil || tr.Usage.Input != 1050 || tr.Usage.Output != 30 {
		t.Errorf("turn = %+v", tr)
	}
	if len(tr.Steps) != 4 || tr.Steps[0].Text != "Let me look." || tr.Steps[1].Name != "read" || tr.Steps[1].Ms != 12 || !*tr.Steps[1].OK ||
		tr.Steps[2].Kind != "search" || tr.Steps[2].Call != "c_search" || tr.Steps[2].Ms != 5200 ||
		tr.Steps[3].Name != "bash" || *tr.Steps[3].OK {
		t.Errorf("steps = %+v", tr.Steps)
	}
}
