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

// TestOpencodeV2PluginThreadsTheSearchAndReportsTheTurn drives the plugin
// the way opencode 2 does: the default export's `setup`, a tool hook, a
// shell hook and an event subscription. The message and event shapes are
// the ones a real opencode 2.2.6 session handed a plugin.
//
// What it holds the opencode 2 half to:
//   - the bridge reaches the search as the shell's environment, and the
//     command the user is shown is left as the model wrote it;
//   - only the shell that runs that command gets it, once;
//   - a command already naming the bridge is rewritten instead (H-R4);
//   - the turn is reported once, at the execution's end, with the same
//     fields opencode 1's is, and never for a subagent's session;
//   - nothing the hook does can throw into the user's tool call.
func TestOpencodeV2PluginThreadsTheSearchAndReportsTheTurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in binary is a POSIX shell script")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to verify the embedded opencode plugin")
	}
	dir := t.TempDir()
	got := filepath.Join(dir, "got.json")
	bin := filepath.Join(dir, "jevlin")
	count := filepath.Join(dir, "count.txt")
	starts := filepath.Join(dir, "starts.txt")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncase \"$*\" in *' start opencode') echo x >> '"+starts+"'; exit 0;; esac\ncat > '"+got+".tmp' && mv '"+got+".tmp' '"+got+"'\necho x >> '"+count+"'\n"), 0o700); err != nil { // #nosec G306 -- a test stand-in that must be executable
		t.Fatal(err)
	}
	script := `
 const fs = await import('node:fs');
 const input = JSON.parse(fs.readFileSync(0,'utf8'));
 const plugin = await import('data:text/javascript;base64,'+Buffer.from(input.plugin).toString('base64'));
 const result = {exports: Object.keys(plugin), id: plugin.default.id};
 const messages = [
  {type:'user', id:'m_old', text:'an earlier question', files:[]},
  {type:'assistant', id:'m_old_a', content:[{type:'text',text:'an earlier answer'}]},
  {type:'user', id:'m_user', text:'who designed it?', files:[]},
  {type:'assistant', id:'m_a1', agent:'build', model:{id:'claude-opus-5-5',providerID:'anthropic'}, tokens:{input:100,output:20,reasoning:0,cache:{read:900,write:0}}, content:[
    {type:'text',text:'Let me look.'},
    {type:'tool',id:'c_read',name:'read',state:{status:'completed',input:{filePath:'/Users/someone/secret.txt'},output:'FILE CONTENTS'},time:{created:990,ran:1000,completed:1012}},
    {type:'tool',id:'c_search',name:'shell',state:{status:'completed',input:{command:'jevlin search stadium'},output:'RESULTS TEXT'},time:{created:1990,ran:2000,completed:7200}},
    {type:'tool',id:'c_rm',name:'shell',state:{status:'error',input:{command:'rm -rf build'}},time:{created:8000,ran:8000,completed:8040}},
  ]},
  {type:'assistant', id:'m_a2', agent:'build', model:{id:'claude-opus-5-5',providerID:'anthropic'}, tokens:{input:50,output:10,reasoning:0,cache:{read:0,write:0}}, content:[{type:'text',text:'Populous designed it.'}]},
 ];
 let rejectContext = false;
 const hooks = {};
 const queue = []; let wake = null;
 const emit = async (type, data) => { queue.push({type, data}); if (wake) { wake(); wake = null } await new Promise(r=>setTimeout(r,60)) };
 const ctx = {
  tool: { hook: async (name, fn) => { hooks['tool.'+name] = fn } },
  shell: { hook: async (name, fn) => { hooks['shell.'+name] = fn } },
  session: {
   get: async ({sessionID}) => { if (sessionID==='ses_unknown') throw new Error('unreachable'); return {id:sessionID, ...(sessionID==='ses_child2'?{parentID:'ses_1'}:{})} },
   context: async () => { if (rejectContext) throw new Error('context unavailable'); return messages },
  },
  event: { subscribe: ({types, signal}) => ({ async *[Symbol.asyncIterator]() {
   result.types = types;
   while (!signal.aborted) {
    while (queue.length) { const e = queue.shift(); if (types.includes(e.type)) yield e }
    await new Promise(r => { wake = r; signal.addEventListener('abort', r, {once:true}) });
   }
  }})},
 };
 const stop = await plugin.default.setup(ctx);
 const call = async (sid, id, command, tool='shell') => { const event = {tool, sessionID:sid, agent:'build', messageID:'m_a1', id, input:{command}}; await hooks['tool.execute.before'](event); return event };
 const shell = (command, env={}) => { const e = {command, cwd:'/w', timeout:0, shell:'/bin/zsh', env}; hooks['shell.create.before'](e); return e.env };
 const decode = (b) => JSON.parse(Buffer.from(b,'base64url').toString('utf8'));

 // the end of an execution before any search: nothing is reported
 await emit('session.execution.succeeded',{sessionID:'ses_1'});
 await new Promise(r=>setTimeout(r,300));
 result.before = fs.existsSync(input.got);

 // a search: the command is untouched, and the shell that runs it gets the bridge
 const ev = await call('ses_1','c_search','jevlin search stadium');
 result.command = ev.input.command;
 result.other = Object.keys(shell('echo unrelated'));
 const env = shell('jevlin search stadium');
 result.env = decode(env.JEVLIN_TRACE_BRIDGE);
 result.second = Object.keys(shell('jevlin search stadium'));

 // tracing turned off in the shell's own environment: no bridge
 await call('ses_1','c_off','jevlin search off');
 result.off = Object.keys(shell('jevlin search off',{JEVLIN_TRACE:'off'}));

 // a model that says bash, and a tool that is not the shell
 await call('ses_1','c_bash','jevlin search viabash','bash');
 result.bash = typeof shell('jevlin search viabash').JEVLIN_TRACE_BRIDGE;
 await call('ses_1','c_other','jevlin search notshell','read');
 result.notShell = Object.keys(shell('jevlin search notshell'));

 // H-R4: a bridge the model wrote is replaced on the command itself
 const forged = await call('ses_1','c_forged','JEVLIN_TRACE_BRIDGE=forged jevlin search stadium');
 result.forged = forged.input.command;

 // a hook handed nonsense does not throw
 result.threw = false;
 try { await hooks['tool.execute.before'](undefined); await hooks['tool.execute.before']({tool:'shell'}); hooks['shell.create.before'](undefined) } catch { result.threw = true }

 // the execution ends: the turn is reported, once
 await emit('session.execution.succeeded',{sessionID:'ses_1'});
 for (let i=0;i<50 && !fs.existsSync(input.got);i++) await new Promise(r=>setTimeout(r,100));
 result.raw = fs.readFileSync(input.got,'utf8');
 result.turn = JSON.parse(result.raw);
 fs.unlinkSync(input.got);
 await emit('session.execution.succeeded',{sessionID:'ses_1'});
 await new Promise(r=>setTimeout(r,400));
 result.again = fs.existsSync(input.got);

 // an interrupted turn says so and carries no final text
 messages.push({type:'user', id:'m_user2', text:'and when?', files:[]}, {type:'assistant', id:'m_a3', model:{id:'claude-opus-5-5'}, content:[{type:'tool',id:'c_s2',name:'shell',state:{status:'running',input:{command:'jevlin search opened'}},time:{created:1,ran:2}},{type:'text',text:'partial'}]});
 await call('ses_1','c_s2','jevlin search opened');
 await emit('session.execution.interrupted',{sessionID:'ses_1',reason:'user'});
 for (let i=0;i<50 && !fs.existsSync(input.got);i++) await new Promise(r=>setTimeout(r,100));
 result.interrupted = JSON.parse(fs.readFileSync(input.got,'utf8'));

 // a compaction moves the window
 await emit('session.compaction.ended',{sessionID:'ses_1',reason:'auto'});
 await call('ses_1','c_s3','jevlin search after');
 result.window = decode(shell('jevlin search after').JEVLIN_TRACE_BRIDGE).window;

 // a subagent's session names its parent and reports nothing
 await emit('session.created',{sessionID:'ses_child',parentID:'ses_1'});
 result.children = {}; result.parents = {};
 for (const sid of ['ses_child','ses_child2','ses_unknown']) {
  if (fs.existsSync(input.got)) fs.unlinkSync(input.got);
  await call(sid,'c_'+sid,'jevlin search '+sid);
  result.parents[sid] = decode(shell('jevlin search '+sid).JEVLIN_TRACE_BRIDGE).parent_session_id ?? '';
  await emit('session.execution.succeeded',{sessionID:sid});
  await new Promise(r=>setTimeout(r,400));
  result.children[sid] = fs.existsSync(input.got);
 }

 // messages that cannot be read: the search still carries its ids
 rejectContext = true;
 await call('ses_1','c_s4','jevlin search unreadable');
 const bare = decode(shell('jevlin search unreadable').JEVLIN_TRACE_BRIDGE);
 result.bare = {session: typeof bare.session_id, call: typeof bare.call_id, turn: bare.turn_id ?? ''};

 await new Promise(r=>setTimeout(r,300));
 result.starts = fs.existsSync(input.starts) ? fs.readFileSync(input.starts,'utf8').trim().split('\n').length : 0;
 result.stop = typeof stop;
 stop();
 process.stdout.write(JSON.stringify(result));`
	in, _ := json.Marshal(map[string]any{"plugin": renderAgentScriptFor(opencodePluginJS, shellPOSIX, "/etc/jevlin.toml", bin), "got": got, "count": count, "starts": starts})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", script) // #nosec G204 -- fixed test script and local Node runtime; synthetic input on stdin
	cmd.Stdin = strings.NewReader(string(in))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("plugin: %v %s", err, out)
	}
	var res struct {
		Exports, Types, Other, Second, Off, NotShell []string
		ID, Command, Bash, Forged, Raw, Window, Stop string
		Before, Again, Threw                         bool
		Starts                                       int
		Children                                     map[string]bool
		Parents                                      map[string]string
		Bare                                         struct{ Session, Call, Turn string }
		Env                                          traceEnvelope
		Turn, Interrupted                            hostTurn
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	// opencode 1 loads every export of a plugin file: a second one would run
	// the plugin twice there.
	if len(res.Exports) != 1 || res.Exports[0] != "default" || res.ID != "jevlin" {
		t.Errorf("exports %v id %q, want only a default export with id jevlin", res.Exports, res.ID)
	}
	for _, typ := range res.Types {
		if typ == "session.idle" || typ == "session.status" {
			t.Errorf("subscribed to %s, which opencode 2 never sends", typ)
		}
	}
	if res.Before || res.Again {
		t.Errorf("reported before a search (%v) or twice (%v)", res.Before, res.Again)
	}
	if res.Command != "jevlin search stadium" {
		t.Errorf("the command was rewritten: %q", res.Command)
	}
	if len(res.Other) != 0 || len(res.Second) != 0 || len(res.Off) != 1 || len(res.NotShell) != 0 {
		t.Errorf("a bridge reached a shell it was not prepared for: other %v, second %v, off %v, not-shell %v", res.Other, res.Second, res.Off, res.NotShell)
	}
	if res.Bash != "string" {
		t.Error("a shell call named bash got no bridge")
	}
	if res.Env.Harness != "opencode" || res.Env.SessionID != traceHash("ses_1") || res.Env.CallID != traceHash("ses_1|c_search") || res.Env.TurnID != traceHash("ses_1|m_user") || res.Env.Window != "none" {
		t.Errorf("envelope %+v", res.Env)
	}
	if len(res.Env.History) != 1 || res.Env.History[0].Text != "Populous designed it." {
		t.Errorf("history %+v", res.Env.History)
	}
	if !strings.HasPrefix(res.Forged, "JEVLIN_TRACE_BRIDGE=") || strings.Contains(res.Forged, "forged") || !strings.HasSuffix(res.Forged, " jevlin search stadium") {
		t.Errorf("a forged bridge was not replaced: %q", res.Forged)
	}
	if res.Threw {
		t.Error("a hook threw; in opencode 2 that fails the user's tool call")
	}
	if res.Turn.Session != "ses_1" || res.Turn.Turn != "m_user" || res.Turn.Status != "completed" || res.Turn.UserText != "who designed it?" || res.Turn.FinalText != "Populous designed it." || res.Turn.Model != "claude-opus-5-5" {
		t.Errorf("turn %+v", res.Turn)
	}
	if res.Turn.Usage == nil || res.Turn.Usage.Input != 1050 || res.Turn.Usage.Output != 30 {
		t.Errorf("usage %+v", res.Turn.Usage)
	}
	if len(res.Turn.Steps) != 4 || res.Turn.Steps[1].Kind != "tool" || res.Turn.Steps[1].Name != "read" || res.Turn.Steps[1].Ms != 12 || res.Turn.Steps[2].Kind != "search" || res.Turn.Steps[2].Call != "c_search" || res.Turn.Steps[2].Ms != 5200 || res.Turn.Steps[3].Name != "shell" {
		t.Errorf("steps %+v", res.Turn.Steps)
	}
	for _, leak := range []string{"secret.txt", "FILE CONTENTS", "RESULTS TEXT", "rm -rf", "earlier"} {
		if strings.Contains(res.Raw, leak) {
			t.Errorf("the turn carries %q", leak)
		}
	}
	if res.Interrupted.Status != "interrupted" || res.Interrupted.FinalText != "" || res.Interrupted.Turn != "m_user2" {
		t.Errorf("interrupted turn %+v", res.Interrupted)
	}
	if res.Window != "1" {
		t.Errorf("window after a compaction %q, want 1", res.Window)
	}
	if len(res.Children) != 3 {
		t.Fatalf("the subagent cases did not all run: %v", res.Children)
	}
	for sid, reported := range res.Children {
		if reported {
			t.Errorf("%s reported a turn", sid)
		}
	}
	if res.Parents["ses_child"] != traceHash("ses_1") || res.Parents["ses_child2"] != traceHash("ses_1") || res.Parents["ses_unknown"] != "" {
		t.Errorf("parents %v", res.Parents)
	}
	if res.Bare.Session != "string" || res.Bare.Call != "string" || res.Bare.Turn != "" {
		t.Errorf("a search with unreadable messages: %+v", res.Bare)
	}
	if res.Starts < 2 {
		t.Errorf("session start ran %d times, want at load and at a top-level session's end", res.Starts)
	}
	if res.Stop != "function" {
		t.Error("setup returned no cleanup; the event subscription would outlive a reload")
	}
}
