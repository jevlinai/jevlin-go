package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jevlinai/jevlin-go/pkg/redact"
)

func traceBoundaryInputs() map[string]string {
	shapes := map[string]string{ // #nosec G101 -- synthetic redaction canaries, including fictional URL userinfo
		"sk": "sk-SyntheticCredentialBody0123456789", "sr": "sr-SyntheticCredentialBody0123456789",
		"github_o": "gho_SyntheticCredentialBody0123456789",
		"github_u": "ghu_SyntheticCredentialBody0123456789",
		"github_s": "ghs_SyntheticCredentialBody0123456789",
		"github_r": "ghr_SyntheticCredentialBody0123456789",
		"sk_or":    "sk-or-v1-SyntheticCredentialBody0123456789",
		"sk_ant":   "sk-ant-SyntheticCredentialBody0123456789",
		"github":   "ghp_SyntheticCredentialBody0123456789", "github_pat": "github_pat_SyntheticCredentialBody0123456789",
		"aws": "AKIA0123456789ABCDEF", "jwt": "eyJSynthetic.SyntheticPayload.SyntheticSignature",
		"userinfo": "https://synthetic:PasswordCanary0123456789@example.test", "email": "syntheticperson012345@example.test",
		"unix_home": "/home/SyntheticPerson012345/private", "mac_home": "/Users/SyntheticPerson012345/private",
		"windows_home": `C:\Users\SyntheticPerson012345\private`,
		// What has no credential shape: a secret known only by its name, an
		// environment dump, and this client's own trace bridge.
		"secret_assignment": "export DATABASE_PASSWORD=HunterCanary0123456789",
		"quoted_assignment": `API_TOKEN="HunterCanary0123456789 two words"`,
		"env_dump":          "\nA_ONE=DumpCanary1\nB_TWO=DumpCanary2\nexport C_THREE=DumpCanary3\nD_FOUR=DumpCanary4\nE_FIVE=DumpCanary5\nF_SIX=DumpCanary6",
		"bridge":            "JEVLIN_TRACE_BRIDGE=BridgeCanary0123456789 jevlin search --stdin",
		// A secret chained behind a name that is not one, PowerShell's
		// spaced assignment, a hyphenated flag, and a quoted value across
		// lines: each is removed whole before the cut, wherever it falls. The
		// last starts its line, the only place a quoted value may run across
		// lines: mid-line its quote may close a string instead.
		"chained_assignment":    "jdbc:postgresql://db/app?user=fred&password=HunterCanary0123456789&ssl=true",
		"powershell_assignment": `$env:API_TOKEN = "HunterCanary0123456789"`,
		"hyphenated_flag":       "curl --api-key=HunterCanary0123456789 https://example.test",
		"quoted_across_lines":   "\nPRIVATE_KEY=\"HunterCanary0123456789\nHunterCanary0123456789\n\" next",
		// A carriage return or U+2028 inside a line ends the line for
		// JavaScript's `.` and not for Go's: with `.` those two lines would
		// not count, the run would fall under five, and nothing would go.
		"env_dump_odd_lines": "\nA_ONE=DumpCanary1\nB_TWO=DumpCanary2\u2028tail\nC_THREE=DumpCanary3\rmid\nD_FOUR=DumpCanary4\nE_FIVE=DumpCanary5",
	}
	out := map[string]string{}
	for name, secret := range shapes {
		for label, cut := range map[string]int{"before": len(secret) + 2, "crossing": len(secret) / 2, "prefix": 1, "beyond": -2} {
			out[name+"/"+label] = "start " + secret + " " + strings.Repeat(".", traceHistoryCap-len(secret)-1+cut)
		}
		out[name+"/multiple"] = "start " + secret + " " + secret + " " + strings.Repeat(".", traceHistoryCap-len(secret))
		out[name+"/utf8"] = "start " + secret + " " + strings.Repeat("界", traceHistoryCap/3-3) + "é"
	}
	return out
}

// The synthetic machine the identity cases run on. The Go scrubber is told
// it through redact.SetLocalIdentity and the JavaScript one through
// prepareTraceHistory's second argument, so no case depends on the name of
// the machine or the account the tests happen to run under.
const (
	syntheticTraceHost    = "SyntheticHost0123.corp.example"
	syntheticTraceAccount = "syntheticacct0123"
)

// traceIdentityInputs are the hostname and the account name where no path
// pattern reaches them, cut at the same boundaries as the other canaries.
func traceIdentityInputs() map[string]string {
	shapes := map[string]string{
		"account_in_env":   "USER=syntheticacct0123",
		"account_in_ssh":   "ssh syntheticacct0123@db1.example.test uptime",
		"hostname_in_text": "logged in on synthetichost0123 as USER=SyntheticAcct0123",
		"account_in_home":  `copied to /mnt/c/Users/SyntheticAcct0123/x and C:\Users\syntheticacct0123\y`,
		"prompt":           "syntheticacct0123@SyntheticHost0123:~$",
	}
	out := map[string]string{}
	for name, secret := range shapes {
		for label, cut := range map[string]int{"before": len(secret) + 2, "crossing": len(secret) / 2, "beyond": -2} {
			out[name+"/"+label] = "start " + secret + " " + strings.Repeat(".", traceHistoryCap-len(secret)-1+cut)
		}
	}
	return out
}

// traceSurvivorInputs are texts the new rules must leave exactly as they
// are, in both languages: the scrubbers agreeing on what to remove is half
// of parity, and agreeing on what to keep is the other half. A rule that is
// looser in the JavaScript copy removes text the Go consumer never would.
func traceSurvivorInputs() map[string]string {
	return map[string]string{ // #nosec G101 -- prose that must NOT be read as credentials
		"survives/lowercase key":  "pass key=value pairs, sort --key=2, and pass=2 of the compiler",
		"survives/not a segment":  "MONKEY=banana TOKENIZER_PATH=/opt/tok JEVLIN_CONFIG=/etc/jevlin.toml",
		"survives/four env lines": "A_ONE=1\nB_TWO=2\nC_THREE=3\nD_FOUR=4\nthen prose",
		"survives/broken run":     "A=1\nB=2\nprose\nC=3\nD=4\nE=5",
		"survives/name in a word": "xsyntheticacct0123 and syntheticacct0123_2 and presynthetichost0123x",
		// The account is removed only where the text uses it as one.
		"survives/account in prose": "syntheticacct0123 wrote this, and SyntheticAcct0123: it is a word here",
		"survives/empty value":      "set API_TOKEN= to clear it",
		// A no-break space is whitespace to JavaScript's \s and not to Go's:
		// with \s the third line would join the run and make it a dump.
		"survives/nbsp breaks a run": "A=1\nB=2\n\u00a0C=3\nD=4\nE=5",
	}
}

func assertPreparedHistory(t *testing.T, text, source string) {
	t.Helper()
	expected := redact.TraceText(source)
	if len(expected) > traceHistoryCap {
		expected = expected[len(expected)-traceHistoryCap:]
		for len(expected) > 0 && !utf8.RuneStart(expected[0]) {
			expected = expected[1:]
		}
	}
	if text != expected {
		t.Fatalf("prepared history differs from complete-source scrub followed by UTF-8 cap (got %d bytes, want %d)\n%s", len(text), len(expected), traceDifference(text, expected))
	}
	if len(text) > traceHistoryCap || !utf8.ValidString(text) {
		t.Fatalf("history exceeds byte cap or is invalid UTF-8: %d", len(text))
	}
	for _, fragment := range []string{"CredentialBody0123456789", "0123456789ABCDEF", "SyntheticPayload", "PasswordCanary0123456789", "syntheticperson012345", "SyntheticPerson012345",
		"HunterCanary0123456789", "DumpCanary", "BridgeCanary0123456789", "SyntheticHost0123", "syntheticacct0123"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(fragment)) {
			t.Fatalf("prepared history retains synthetic marker %q", fragment)
		}
	}
}

// traceDifference shows where two texts part, bounded on both sides, so a
// parity failure says what differs rather than only that the lengths do.
func traceDifference(got, want string) string {
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	from := max(0, i-40)
	window := func(s string) string {
		return s[min(from, len(s)):min(i+80, len(s))]
	}
	return fmt.Sprintf("first difference at byte %d:\n  got:  %q\n  want: %q", i, window(got), window(want))
}

func TestTraceRedactionBoundaries(t *testing.T) {
	for name, text := range traceBoundaryInputs() {
		t.Run(name, func(t *testing.T) {
			// Each input has its own fake filesystem and reads no shared
			// state, and under the race detector this is the slowest test in
			// the package by minutes, so the inputs run side by side.
			t.Parallel()
			t.Run("envelope", func(t *testing.T) {
				env := capTrace(&traceEnvelope{V: 1, History: []traceHistory{{Role: "assistant", Text: text}}})
				if env == nil || len(env.History) != 1 {
					t.Fatal("ordinary bounded source lost history")
				}
				assertPreparedHistory(t, env.History[0].Text, text)
			})
			t.Run("transcript", func(t *testing.T) {
				fs, ops := newFakeHookOps(nil)
				record, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}})
				fs.files["/transcript"] = append([]byte("{\"type\":\"user\",\"message\":{\"content\":\"query\"}}\n"), record...)
				env := capTrace(&traceEnvelope{V: 1, History: []traceHistory{{Role: "assistant", Text: currentAssistantText(ops, hookPayload{TranscriptPath: "/transcript"})}}})
				if env == nil || len(env.History) != 1 || env.History[0].Text == "" {
					t.Fatal("bounded transcript lost history")
				}
				assertPreparedHistory(t, env.History[0].Text, text)
			})
			t.Run("cursor", func(t *testing.T) {
				_, ops := newFakeHookOps(nil)
				runHook(t, ops, hookContext{sessionsDir: "/sessions"}, "cursor afterAgentResponse", map[string]any{"conversation_id": "synthetic", "cwd": "/workspace", "text": text})
				l, ok := loadLineage(ops, conversationLineagePath("/sessions", "/workspace", "synthetic"))
				if !ok || len(l.History) != 1 {
					t.Fatal("missing history")
				}
				assertPreparedHistory(t, l.History[0].Text, text)
			})
		})
	}
}

// The hostname and the account name are removed on the Go path too, from
// the same complete source and before the cut.
func TestTraceRemovesTheLocalIdentity(t *testing.T) {
	defer redact.SetLocalIdentity(syntheticTraceHost, syntheticTraceAccount)()
	for name, text := range traceIdentityInputs() {
		t.Run(name, func(t *testing.T) {
			env := capTrace(&traceEnvelope{V: 1, History: []traceHistory{{Role: "assistant", Text: text}}})
			if env == nil || len(env.History) != 1 {
				t.Fatal("ordinary bounded source lost history")
			}
			assertPreparedHistory(t, env.History[0].Text, text)
			if final, _, _ := prepareFinalText(text); final != env.History[0].Text {
				t.Fatal("the turn end's final text is prepared differently from trace history")
			}
		})
	}
}

// opencode runs a subagent as a child session. The plugin names the parent
// from the session's creation event when it saw one, asks opencode once when
// it did not, and sends a search without a parent — never without a trace —
// when it cannot find out.
func TestOpencodeSubagentNamesItsParent(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to verify the embedded opencode plugin")
	}
	script := `
 const fs = await import('node:fs');
 const input = JSON.parse(fs.readFileSync(0,'utf8'));
 const plugin = await import('data:text/javascript;base64,'+Buffer.from(input.plugin).toString('base64'));
 const sessions = {root:{id:'root'}, child:{id:'child', parentID:'root'}, self:{id:'self', parentID:'self'}};
 let gets = 0; let flaky = 0;
 const client = {session:{
  messages: async()=>({data:[]}),
  get: async({path})=>{
   gets++;
   if (path.id==='broken') throw new Error('unreadable');
   // opencode answers an HTTP error with {error} and no data, without throwing.
   if (path.id==='flaky' && flaky++ === 0) return {error:{name:'UnknownError'}};
   if (path.id==='flaky') return {data:{id:'flaky', parentID:'root'}};
   return {data:sessions[path.id]};
  },
 }};
 const hooks = await plugin.JevlinLineage({client});
 const search = async (sid) => {
  const output={args:{command:'jevlin search query'}};
  await hooks['tool.execute.before']({tool:'bash',sessionID:sid,callID:'call'},output);
  return JSON.parse(Buffer.from(output.args.command.split(' ')[0].split('=')[1],'base64url').toString('utf8'));
 };
 const result = {};
 result.root = await search('root');
 result.child = await search('child');
 result.child_again = await search('child');
 result.gets_after_two_children = gets;
 result.self = await search('self');
 result.broken = await search('broken');
 result.flaky_first = await search('flaky');
 result.flaky_second = await search('flaky');
 await hooks.event({event:{type:'session.created', properties:{info:{id:'seen', parentID:'root'}}}});
 const before = gets;
 result.seen = await search('seen');
 result.asked_for_seen = gets - before;
 process.stdout.write(JSON.stringify(result));`
	input, _ := json.Marshal(map[string]any{"plugin": renderAgentScript(opencodePluginJS, shellPOSIX, testCfg)})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", script) // #nosec G204 -- fixed test script and local Node runtime; synthetic input on stdin
	cmd.Stdin = strings.NewReader(string(input))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("plugin: %v %s", err, output)
	}
	var got struct {
		Root, Child, ChildAgain, Self, Broken, Seen traceEnvelope
		FlakyFirst, FlakySecond                     traceEnvelope
		Gets                                        int `json:"gets_after_two_children"`
		AskedForSeen                                int `json:"asked_for_seen"`
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(output, &raw); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	for name, dst := range map[string]any{
		"root": &got.Root, "child": &got.Child, "child_again": &got.ChildAgain, "self": &got.Self,
		"broken": &got.Broken, "seen": &got.Seen, "flaky_first": &got.FlakyFirst, "flaky_second": &got.FlakySecond, "gets_after_two_children": &got.Gets, "asked_for_seen": &got.AskedForSeen,
	} {
		if err := json.Unmarshal(raw[name], dst); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	parent := traceHash("root")
	if got.Root.SessionID != parent || got.Root.ParentSessionID != "" {
		t.Errorf("root: session=%q parent=%q", got.Root.SessionID, got.Root.ParentSessionID)
	}
	// The child's parent is the id the root's own searches carry.
	if got.Child.SessionID != traceHash("child") || got.Child.ParentSessionID != parent || got.ChildAgain.ParentSessionID != parent {
		t.Errorf("child: session=%q parent=%q, again parent=%q", got.Child.SessionID, got.Child.ParentSessionID, got.ChildAgain.ParentSessionID)
	}
	// root once, child once: the second child search asked nothing.
	if got.Gets != 2 {
		t.Errorf("opencode was asked %d times for two sessions", got.Gets)
	}
	if got.Self.ParentSessionID != "" {
		t.Errorf("a session named itself as parent: %q", got.Self.ParentSessionID)
	}
	// Unreadable: the search still carries its trace, with no parent.
	if got.Broken.SessionID != traceHash("broken") || got.Broken.ParentSessionID != "" {
		t.Errorf("unreadable session: session=%q parent=%q", got.Broken.SessionID, got.Broken.ParentSessionID)
	}
	// An error answered without throwing is not "this session has no parent":
	// that search goes out without one, and the next asks again and finds it.
	if got.FlakyFirst.SessionID != traceHash("flaky") || got.FlakyFirst.ParentSessionID != "" || got.FlakySecond.ParentSessionID != parent {
		t.Errorf("error response: first parent=%q, second parent=%q", got.FlakyFirst.ParentSessionID, got.FlakySecond.ParentSessionID)
	}
	if got.Seen.ParentSessionID != parent || got.AskedForSeen != 0 {
		t.Errorf("session seen at creation: parent=%q, asked %d times", got.Seen.ParentSessionID, got.AskedForSeen)
	}
}

func TestOpencodeRedactionBoundaries(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to verify the embedded opencode plugin")
	}
	script := `
 const fs = await import('node:fs');
 const input = JSON.parse(fs.readFileSync(0,'utf8'));
 const plugin = await import('data:text/javascript;base64,'+Buffer.from(input.plugin).toString('base64'));
 const result={};
 for (const [name,text] of Object.entries(input.cases)) {
  const hooks=await plugin.JevlinLineage({client:{session:{messages:async()=>({data:[{info:{role:'assistant'},parts:[{type:'text',text}]}]})}}});
  const output={args:{command:'jevlin search query'}};
  await hooks['tool.execute.before']({tool:'bash',sessionID:'synthetic',callID:'call'},output);
  const bridge=output.args.command.split(' ')[0].split('=')[1];
  result[name]=JSON.parse(Buffer.from(bridge,'base64url').toString('utf8'));
 }
 process.stdout.write(JSON.stringify(result));`
	cases := traceBoundaryInputs()
	cases["source_budget"] = strings.Repeat("x", hookTailBytes+1)
	cases["envelope_budget"] = strings.Repeat("\"", traceHistoryCap)
	cases["max_source"] = strings.Repeat("x", hookTailBytes)
	cases["punctuated_source"] = strings.Repeat("x.", hookTailBytes/2)
	cases["userinfo_numeric_prefix"] = "123https://synthetic:PasswordCanary0123456789@example.test"
	cases["email_leading_punctuation"] = ".+syntheticperson012345@example.test"
	cases["ordinary_prose"] = "bearer tokens expire soon; ssh deploy@example.test; git@example.test:repo; https://example.test/home/page"
	cases["utf8_tail"] = strings.Repeat("界", traceHistoryCap/3+2)
	// The rendered artifact — the template with the shared trace-preparation
	// source spliced in — is what `agents install` actually writes, so it is
	// what this acceptance test executes. Every assertion below is unchanged
	// from before the shared source existed: the migration is mechanical or
	// this test says otherwise.
	input, _ := json.Marshal(map[string]any{"plugin": renderAgentScript(opencodePluginJS, shellPOSIX, testCfg), "cases": cases})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", script) // #nosec G204 -- fixed test script and local Node runtime; synthetic input on stdin
	cmd.Stdin = strings.NewReader(string(input))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("plugin: %v %s", err, output)
	}
	var results map[string]traceEnvelope
	if err := json.Unmarshal(output, &results); err != nil {
		t.Fatal(err)
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			env, ok := results[name]
			if name == "source_budget" || name == "envelope_budget" {
				if !ok || len(env.History) != 0 {
					t.Fatal("over-budget plugin source retained history")
				}
				return
			}
			if !ok || len(env.History) != 1 {
				t.Fatal("bounded plugin source lost history")
			}
			assertPreparedHistory(t, env.History[0].Text, text)
		})
	}
}

// The single-source-of-truth guard. Each JavaScript host keeps its own
// standalone artifact — that is what the host loads — but the scrub and the
// caps inside it are spliced from one file. What makes that real rather
// than aspirational is that neither template contains the logic at all: a
// drifted second copy cannot exist in a file that has no copy.
func TestEveryJSHostRendersTheSharedTraceSource(t *testing.T) {
	shared := strings.TrimRight(agentTraceCommonJS, "\n")
	for name, template := range map[string]string{"opencode": opencodePluginJS, "pi": piExtensionTS} {
		t.Run(name, func(t *testing.T) {
			if n := strings.Count(template, traceCommonMarker); n != 1 {
				t.Fatalf("template carries the trace-common marker %d times, want exactly 1", n)
			}
			rendered := renderAgentScript(template, shellPOSIX, testCfg)
			if !strings.Contains(rendered, shared) {
				t.Fatal("the rendered artifact does not contain the shared source verbatim")
			}
			if strings.Contains(rendered, traceCommonMarker) {
				t.Fatal("the rendered artifact still carries an unexpanded marker")
			}
			// The template's own text must hold none of the shared logic:
			// no redaction, no caps, no second recognizer.
			for _, forked := range []string{"[REDACTED]", "32 * 1024", "48 * 1024", "256 * 1024", "createHash", "SEARCH_RE ="} {
				if strings.Contains(template, forked) {
					t.Errorf("the %s template carries its own copy of %q instead of using the shared source", name, forked)
				}
			}
			// And the rendered artifact must have exactly one of each.
			for _, once := range []string{"const scrubTraceText", "const prepareTraceHistory", "const traceBridge", "const SEARCH_RE"} {
				if n := strings.Count(rendered, once); n != 1 {
					t.Errorf("rendered %s has %d definitions of %q, want 1", name, n, once)
				}
			}
		})
	}
}

// The shared source, executed directly: the same canaries the Go and
// opencode boundary tests use, plus the four budget edges, asserted against
// the functions both adapters actually call.
func TestSharedTraceSourceRedactionBoundaries(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to verify the shared trace-preparation source")
	}
	script := `
 const fs = await import('node:fs');
 const input = JSON.parse(fs.readFileSync(0,'utf8'));
 const src = input.shared + "\nexport { prepareTraceHistory, traceBridge, needsTraceBridge, traceHash, traceIdentityPatterns };";
 const m = await import('data:text/javascript;base64,'+Buffer.from(src).toString('base64'));
 const identity = m.traceIdentityPatterns(input.host, input.account);
 const result = {};
 for (const [name, parts] of Object.entries(input.cases)) {
  const text = m.prepareTraceHistory(parts, identity);
  const env = { v: 1, harness: 'test', session_id: m.traceHash('s') };
  if (text) env.history = [{ role: 'assistant', text }];
  result[name] = { text, bridge: m.traceBridge(env), history: env.history ? env.history.length : 0 };
 }
 // An envelope too large even with no history at all: no bridge.
 const huge = { v: 1, harness: 'test', session_id: 'x'.repeat(input.envelopeCap + 1) };
 result['__no_bridge'] = { bridge: m.traceBridge(huge), history: huge.history ? 1 : 0 };
 process.stdout.write(JSON.stringify(result));`

	// Both scrubbers are given the same synthetic machine, so the identity
	// cases compare like for like wherever the test runs.
	defer redact.SetLocalIdentity(syntheticTraceHost, syntheticTraceAccount)()
	textCases := traceBoundaryInputs()
	for name, text := range traceIdentityInputs() {
		textCases[name] = text
	}
	survivors := traceSurvivorInputs()
	for name, text := range survivors {
		if got := redact.TraceText(text); got != text {
			t.Fatalf("%s: the Go scrubber altered a text this test needs it to keep: %q", name, got)
		}
	}
	cases := map[string][]map[string]string{}
	for name, text := range survivors {
		cases[name] = []map[string]string{{"type": "text", "text": text}}
	}
	for name, text := range textCases {
		cases[name] = []map[string]string{{"type": "text", "text": text}}
	}
	// The complete source budget is measured in BYTES across all parts,
	// joined with one newline each — over it, the entry is omitted whole
	// rather than sliced, because slicing is what hides a severed secret
	// from the scrubber.
	cases["source_budget"] = []map[string]string{{"type": "text", "text": strings.Repeat("x", hookTailBytes+1)}}
	cases["max_source"] = []map[string]string{{"type": "text", "text": strings.Repeat("x", hookTailBytes)}}
	cases["source_budget_across_parts"] = []map[string]string{
		{"type": "text", "text": strings.Repeat("x", hookTailBytes/2)},
		{"type": "text", "text": strings.Repeat("y", hookTailBytes/2)},
	}
	cases["utf8_source_budget"] = []map[string]string{{"type": "text", "text": strings.Repeat("界", hookTailBytes/3+1)}}
	cases["history_cap"] = []map[string]string{{"type": "text", "text": strings.Repeat("z", traceHistoryCap*2)}}
	cases["envelope_drops_history"] = []map[string]string{{"type": "text", "text": strings.Repeat("\"", traceHistoryCap)}}

	input, _ := json.Marshal(map[string]any{"shared": agentTraceCommonJS, "cases": cases, "envelopeCap": traceEnvelopeCap,
		"host": syntheticTraceHost, "account": syntheticTraceAccount})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", script) // #nosec G204 -- fixed test script and local Node runtime; synthetic input on stdin
	cmd.Stdin = strings.NewReader(string(input))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shared source: %v\n%s", err, output)
	}
	var results map[string]struct {
		Text    *string `json:"text"`
		Bridge  *string `json:"bridge"`
		History int     `json:"history"`
	}
	if err := json.Unmarshal(output, &results); err != nil {
		t.Fatal(err)
	}

	for name, text := range textCases {
		t.Run(name, func(t *testing.T) {
			got := results[name]
			if got.Text == nil {
				t.Fatal("a bounded source was omitted whole")
			}
			assertPreparedHistory(t, *got.Text, text)
		})
	}
	for name, text := range survivors {
		t.Run(name, func(t *testing.T) {
			if got := results[name]; got.Text == nil || *got.Text != text {
				t.Fatalf("the JavaScript scrubber altered a text the Go scrubber keeps:\n  in:  %q\n  out: %v", text, got.Text)
			}
		})
	}
	t.Run("over the source budget is omitted whole", func(t *testing.T) {
		for _, name := range []string{"source_budget", "source_budget_across_parts", "utf8_source_budget"} {
			if got := results[name]; got.Text != nil {
				t.Errorf("%s: sliced an over-budget source instead of omitting it (%d bytes kept)", name, len(*got.Text))
			}
		}
		if results["max_source"].Text == nil {
			t.Error("a source exactly at the budget was omitted")
		}
	})
	t.Run("history is capped at the tail", func(t *testing.T) {
		got := results["history_cap"]
		if got.Text == nil || len(*got.Text) != traceHistoryCap {
			t.Fatalf("history was not capped to %d bytes: %v", traceHistoryCap, got.Text)
		}
	})
	t.Run("an oversized envelope drops history first", func(t *testing.T) {
		got := results["envelope_drops_history"]
		if got.Bridge == nil {
			t.Fatal("no bridge at all, when dropping history would have been enough")
		}
		env := decodeTraceBridge(*got.Bridge)
		if env == nil || len(env.History) != 0 {
			t.Fatalf("history survived an oversized envelope: %+v", env)
		}
	})
	t.Run("an envelope oversized without history yields no bridge", func(t *testing.T) {
		if b := results["__no_bridge"].Bridge; b != nil {
			t.Fatalf("a bridge was built past the envelope cap (%d bytes)", len(*b))
		}
	})
}

func TestTraceSourceBudget(t *testing.T) {
	text := strings.Repeat("x", hookTailBytes+1)
	if got := prepareTraceText(text); got != "" {
		t.Fatal("oversize source retained history")
	}
	fs, ops := newFakeHookOps(nil)
	record, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}})
	fs.files["/transcript"] = append([]byte("{\"type\":\"user\",\"message\":{\"content\":\"q\"}}\n"), record...)
	if got := currentAssistantText(ops, hookPayload{TranscriptPath: "/transcript"}); got != "" {
		t.Fatal("incomplete transcript entry retained history")
	}
}
