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

	"github.com/jevlinai/jevlin-go/pkg/redact"
)

// sharedTraceCase is one row of pkg/redact/testdata/trace_cases.json, the
// table both scrubbers are held to: pkg/redact's TestTraceCases runs it
// through the Go scrubber, and this file runs it through the JavaScript one.
type sharedTraceCase struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Account string `json:"account"`
	In      string `json:"in"`
	Want    string `json:"want"`
}

func loadSharedTraceCases(t *testing.T) []sharedTraceCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "redact", "testdata", "trace_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []sharedTraceCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no shared trace cases")
	}
	return cases
}

// runSharedTraceSource runs script under node with the shared trace source
// importable as `m`, input on stdin as JSON, and decodes what it prints.
func runSharedTraceSource(t *testing.T, script string, input any, out any) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to verify the shared trace-preparation source")
	}
	prelude := `
 const fs = await import('node:fs');
 const input = JSON.parse(fs.readFileSync(0,'utf8'));
 const src = input.shared + "\nexport { prepareTraceHistory, scrubTraceText, traceIdentityPatterns, withTraceBridge };";
 const m = await import('data:text/javascript;base64,'+Buffer.from(src).toString('base64'));
`
	payload, err := json.Marshal(map[string]any{"shared": agentTraceCommonJS, "input": input})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", prelude+script) // #nosec G204 -- fixed test script and local Node runtime; synthetic input on stdin
	cmd.Stdin = strings.NewReader(string(payload))
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("shared source: %v\n%s", err, output)
	}
	if err := json.Unmarshal(output, out); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
}

// Every row of the shared table through the JavaScript scrubber, each with
// its own identity, compared with the row's own answer and with the Go
// scrubber's. The rows are what make a JavaScript-only change visible: a
// secret word dropped from one list, a name matched case-sensitively, a
// carriage return lost from a dump, a generic name searched for, a domain
// prefix kept, a short name allowed, a dot left unescaped.
func TestSharedTraceSourceAgreesOnEveryTraceCase(t *testing.T) {
	cases := loadSharedTraceCases(t)
	script := `
 const out = input.input.map((c) => m.prepareTraceHistory([{ type: 'text', text: c.in }], m.traceIdentityPatterns(c.host, c.account)));
 process.stdout.write(JSON.stringify(out));`
	var js []*string
	runSharedTraceSource(t, script, cases, &js)
	if len(js) != len(cases) {
		t.Fatalf("%d answers for %d cases", len(js), len(cases))
	}
	for i, c := range cases {
		restore := redact.SetLocalIdentity(c.Host, c.Account)
		goText := redact.TraceText(c.In)
		restore()
		if goText != c.Want {
			t.Errorf("%s: the Go scrubber does not give the table's answer\n%s", c.Name, traceDifference(goText, c.Want))
		}
		if js[i] == nil {
			t.Errorf("%s: the JavaScript scrubber omitted a bounded source", c.Name)
			continue
		}
		if *js[i] != c.Want {
			t.Errorf("%s: the JavaScript scrubber does not give the table's answer\n  in: %q\n%s", c.Name, c.In, traceDifference(*js[i], c.Want))
		}
	}
}

// The bridge this client writes, in each shell's syntax, loses its value in
// both scrubbers. The commands come from the renderers themselves (Go's
// withTraceBridge and the shared source's), never typed here, so the test
// follows what the client actually writes: PowerShell's form has spaces
// around `=`, which a rule that wanted NAME=value would let through.
func TestTheRenderedBridgeLosesItsValue(t *testing.T) {
	const bridge = "eyJCanaryBridgeValue0123"
	const search = "jevlin search --stdin"
	shells := map[string]shellKind{"powershell": shellPowerShell, "posix": shellPOSIX}
	var js map[string]struct{ Rendered, Scrubbed string }
	runSharedTraceSource(t, `
 const out = {};
 for (const shell of ['powershell', 'posix']) {
  const rendered = m.withTraceBridge(input.input.search, input.input.bridge, shell);
  out[shell] = { rendered, scrubbed: m.scrubTraceText(rendered, m.traceIdentityPatterns('', '')) };
 }
 process.stdout.write(JSON.stringify(out));`, map[string]string{"bridge": bridge, "search": search}, &js)
	defer redact.SetLocalIdentity("", "")()
	for name, sh := range shells {
		rendered, ok := withTraceBridge(sh, bridge, search)
		if !ok || !strings.Contains(rendered, bridge) {
			t.Fatalf("%s: the Go renderer wrote no bridge: %q", name, rendered)
		}
		if js[name].Rendered != rendered {
			t.Fatalf("%s: the two renderers disagree:\n  go: %q\n  js: %q", name, rendered, js[name].Rendered)
		}
		scrubbed := redact.TraceText(rendered)
		if strings.Contains(scrubbed, bridge) || !strings.Contains(scrubbed, search) {
			t.Errorf("%s: the bridge value survived, or more than it went: %q -> %q", name, rendered, scrubbed)
		}
		if js[name].Scrubbed != scrubbed {
			t.Errorf("%s: the scrubbers disagree on the rendered bridge\n%s", name, traceDifference(js[name].Scrubbed, scrubbed))
		}
	}
}

// The shared source finds the account where pkg/redact does: USERNAME on
// Windows, USER then LOGNAME elsewhere, then the home directory's last
// element. Each case runs node in an environment that holds only what the
// case names, so the answer cannot come from the user database.
func TestTheSharedSourceFindsTheAccountAsGoDoes(t *testing.T) {
	type accountCase struct {
		env  map[string]string
		want string
	}
	home := filepath.Join(t.TempDir(), "homeacct")
	cases := map[string]accountCase{
		"USER first":              {map[string]string{"USER": "ann", "LOGNAME": "bob", "HOME": home}, "ann"},
		"then LOGNAME":            {map[string]string{"LOGNAME": "bob", "HOME": home}, "bob"},
		"then the home directory": {map[string]string{"HOME": home}, "homeacct"},
	}
	if runtime.GOOS == "windows" {
		cases = map[string]accountCase{
			"USERNAME, not USER":      {map[string]string{"USERNAME": "John Smith", "USER": "ann", "USERPROFILE": home}, "John Smith"},
			"then the home directory": {map[string]string{"USER": "ann", "USERPROFILE": home}, "homeacct"},
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to verify the shared trace-preparation source")
	}
	// The source goes to node as a file: it is longer than a Windows command
	// line may be, so "-e <source>" fails to start there.
	script := filepath.Join(t.TempDir(), "account.mjs")
	if err := os.WriteFile(script, []byte(agentTraceCommonJS+"\nprocess.stdout.write(JSON.stringify(traceLocalAccount()));"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range cases {
		var env []string
		for _, kv := range os.Environ() {
			k, _, _ := strings.Cut(kv, "=")
			switch strings.ToUpper(k) {
			case "USER", "LOGNAME", "USERNAME", "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH":
				continue
			}
			env = append(env, kv)
		}
		for k, v := range tc.env {
			env = append(env, k+"="+v)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, node, script) // #nosec G204 -- fixed test script and local Node runtime
		cmd.Env = env
		out, err := cmd.Output()
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var got string
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
		if got != tc.want {
			t.Errorf("%s: the shared source found the account %q, want %q", name, got, tc.want)
		}
	}
}

// What the installed adapters run is the shared source with no identity
// argument: the names of the machine it is on. This holds that default to
// Go's on a text naming this machine's own host and account in every
// context the rules cover, so an adapter whose default identity found
// nothing, or found other names than Go does, fails here and not only in a
// golden snapshot. Where this machine's names are both generic or too short
// to be searched for, there is nothing to compare and the test says so.
func TestBothScrubbersRemoveThisMachinesOwnNames(t *testing.T) {
	host, _ := os.Hostname()
	label, _, _ := strings.Cut(host, ".")
	account := ""
	names := []string{"USER", "LOGNAME"}
	if runtime.GOOS == "windows" {
		names = []string{"USERNAME"}
	}
	for _, name := range names {
		if account = os.Getenv(name); account != "" {
			break
		}
	}
	if account == "" {
		if dir, err := os.UserHomeDir(); err == nil {
			account = filepath.Base(dir)
		}
	}
	text := "built on " + label + " today; ssh " + account + "@db1; USER=" + account + "; cp /mnt/c/Users/" + account + "/x ."
	searched := func(h, a string) bool {
		defer redact.SetLocalIdentity(h, a)()
		return redact.TraceText(text) != text
	}
	hostSearched, accountSearched := searched(label, ""), searched("", account)
	if !hostSearched && !accountSearched {
		t.Skipf("this machine's host label %q and account %q are both generic or too short to be searched for", label, account)
	}
	goText := redact.TraceText(text)
	if hostSearched && strings.Contains(strings.ToLower(goText), "built on "+strings.ToLower(label)) {
		t.Errorf("the Go scrubber's default identity kept this machine's host label: %q", goText)
	}
	if accountSearched && strings.Contains(goText, "USER="+account) {
		t.Errorf("the Go scrubber's default identity kept this machine's account: %q", goText)
	}
	var js []*string
	runSharedTraceSource(t, `
 process.stdout.write(JSON.stringify([m.prepareTraceHistory([{ type: 'text', text: input.input }])]));`, text, &js)
	if len(js) != 1 || js[0] == nil {
		t.Fatalf("the shared source omitted the text: %v", js)
	}
	if *js[0] != goText {
		t.Errorf("the two scrubbers' default identities disagree\n%s", traceDifference(*js[0], goText))
	}
}

// sharedTraceLinearBound is how long the shared source's scrub may take over
// 256 KiB of any row of pkg/redact/testdata/trace_slow_inputs.json, which
// pkg/redact's TestTraceTextIsLinearOnAdversarialInputs holds Go to. Before
// they were made linear the fastest of them took 4.7 s here (an address
// after an address) and the slowest 168 s (blanks before a few addresses);
// after, each takes under 15 ms. So the bound fails the quadratic code by
// ten times and passes the linear code by more than twenty on the machine
// that measured it. The scrub runs synchronously in the opencode plugin and
// the Pi extension, before the shell tool, so slow here is a delayed search.
const sharedTraceLinearBound = 400 * time.Millisecond

// Each slow input, built to 256 KiB, through the JavaScript scrubber in a
// node of its own, timed inside node so its start-up is not counted: the
// best of three runs against sharedTraceLinearBound. A node that has not
// finished in fifteen seconds is stopped, and that is a failure too.
func TestTheSharedSourceScrubsAdversarialInputsInLinearTime(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "redact", "testdata", "trace_slow_inputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inputs []struct{ Name, Prefix, Unit, Suffix string }
	if err := json.Unmarshal(raw, &inputs); err != nil {
		t.Fatal(err)
	}
	if len(inputs) == 0 {
		t.Fatal("no slow inputs")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to verify the shared trace-preparation source")
	}
	script := `
 const fs = await import('node:fs');
 const input = JSON.parse(fs.readFileSync(0,'utf8'));
 const src = input.shared + "\nexport { scrubTraceText, traceIdentityPatterns, TRACE_SOURCE_CAP };";
 const m = await import('data:text/javascript;base64,'+Buffer.from(src).toString('base64'));
 const c = input.input;
 const n = Math.floor((m.TRACE_SOURCE_CAP - Buffer.byteLength(c.Prefix) - Buffer.byteLength(c.Suffix)) / Buffer.byteLength(c.Unit));
 const text = c.Prefix + c.Unit.repeat(n) + c.Suffix;
 const id = m.traceIdentityPatterns('', '');
 let best = Infinity;
 for (let i = 0; i < 3 && best > input.boundMs; i++) {
  const start = performance.now();
  m.scrubTraceText(text, id);
  best = Math.min(best, performance.now() - start);
 }
 process.stdout.write(JSON.stringify({ bytes: Buffer.byteLength(text), ms: best }));`
	for _, in := range inputs {
		payload, err := json.Marshal(map[string]any{"shared": agentTraceCommonJS, "input": in, "boundMs": sharedTraceLinearBound.Milliseconds()})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", script) // #nosec G204 -- fixed test script and local Node runtime; synthetic input on stdin
		cmd.Stdin = strings.NewReader(string(payload))
		output, err := cmd.Output()
		timedOut := ctx.Err() != nil
		cancel()
		if timedOut {
			t.Errorf("%s: the shared source had not finished after 15 s", in.Name)
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v\n%s", in.Name, err, output)
		}
		var got struct {
			Bytes int
			Ms    float64
		}
		if err := json.Unmarshal(output, &got); err != nil {
			t.Fatalf("%s: %v: %s", in.Name, err, output)
		}
		if got.Bytes < 255*1024 {
			t.Fatalf("%s: built only %d bytes", in.Name, got.Bytes)
		}
		if bound := float64(sharedTraceLinearBound.Milliseconds()); got.Ms > bound {
			t.Errorf("%s: the shared source took %.0f ms over %d bytes, bound %.0f ms", in.Name, got.Ms, got.Bytes, bound)
		}
	}
}

// pkg/redact's TestNoPromptLineReachesTheNextLine, for the JavaScript
// scrubber: every row of the shared table that removes something, on the
// line after each prompt line of trace_prompt_lines.json, gives exactly the
// row's own answer after the prompt line.
func TestTheSharedSourceLetsNoPromptLineReachTheNextLine(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "redact", "testdata", "trace_prompt_lines.json"))
	if err != nil {
		t.Fatal(err)
	}
	var prompts []string
	if err := json.Unmarshal(raw, &prompts); err != nil {
		t.Fatal(err)
	}
	type pair struct {
		Host, Account, In, Want, Name string
	}
	var pairs []pair
	for _, c := range loadSharedTraceCases(t) {
		if c.Want == c.In {
			continue
		}
		for _, p := range prompts {
			pairs = append(pairs, pair{c.Host, c.Account, p + "\n" + c.In, c.Want, c.Name + " after " + p})
		}
	}
	var js []string
	runSharedTraceSource(t, `
 const out = input.input.map((c) => m.scrubTraceText(c.In, m.traceIdentityPatterns(c.Host, c.Account)));
 process.stdout.write(JSON.stringify(out));`, pairs, &js)
	if len(js) != len(pairs) {
		t.Fatalf("%d answers for %d pairs", len(js), len(pairs))
	}
	for i, p := range pairs {
		if _, after, _ := strings.Cut(js[i], "\n"); after != p.Want {
			t.Errorf("%s:\n  got:  %q\n  want: %q", p.Name, after, p.Want)
		}
	}
}
