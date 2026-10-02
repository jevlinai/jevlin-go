package main

// The bridge, in each shell's syntax, and whose bridge it is (H-R4).
//
// Two copies of this logic exist on purpose — Go for the hooks this binary
// runs, JavaScript for the adapters that run inside their hosts — so the
// pinning test below is what keeps them one rule rather than two that drift,
// the same way SEARCH_RE is pinned to searchCommandRe.

import (
	"context"
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

const foreignBridge = "NOTOURSBRIDGEVALUE"

// The syntax follows the shell, and the PowerShell form clears the variable
// after the call rather than pretending a block scopes it.
func TestBridgeSyntaxFollowsTheShell(t *testing.T) {
	const cmd = "jevlin search --stdin"
	posix, ok := withTraceBridge(shellPOSIX, "OURS", cmd)
	if !ok || posix != bridgeEnv+"=OURS "+cmd {
		t.Fatalf("POSIX bridge: %q (ok=%v)", posix, ok)
	}
	ps, ok := withTraceBridge(shellPowerShell, "OURS", cmd)
	if !ok {
		t.Fatal("no PowerShell bridge")
	}
	if !strings.HasPrefix(ps, "$env:"+bridgeEnv+" = 'OURS'; try { ") {
		t.Fatalf("PowerShell bridge does not assign then guard: %q", ps)
	}
	// Measured on both editions: `& { $env:X=… ; … }` leaves X set in the
	// shell afterwards, because $env: is the process environment and a
	// PowerShell scope does not cover it. The removal is what makes the
	// variable belong to this call only.
	if !strings.Contains(ps, "finally { Remove-Item Env:"+bridgeEnv) {
		t.Fatalf("PowerShell bridge does not remove the variable: %q", ps)
	}
	if strings.HasPrefix(ps, "& {") {
		t.Fatalf("a block does not scope $env:, and this form relies on one: %q", ps)
	}
	// A shell with no form of its own is never guessed at.
	if _, ok := withTraceBridge(shellCmd, "OURS", cmd); ok {
		t.Fatal("cmd got a bridge form nobody proved")
	}
}

// H-R4: a bridge the adapter did not write is removed and replaced, in every
// declared syntax, so the harness on the envelope is only ever ours when we
// put it there.
func TestBridgeReplacesOnesItDidNotWrite(t *testing.T) {
	const search = "jevlin search --stdin"
	for name, before := range map[string]string{
		"POSIX assignment":      bridgeEnv + "=" + foreignBridge + " " + search,
		"two POSIX assignments": bridgeEnv + "=" + foreignBridge + " " + bridgeEnv + "=alsonotours " + search,
		"PowerShell statement":  "$env:" + bridgeEnv + " = '" + foreignBridge + "'\n" + search,
		"PowerShell semicolon":  "$env:" + bridgeEnv + "='" + foreignBridge + "'; " + search,
		"cmd set":               "set " + bridgeEnv + "=" + foreignBridge + " && " + search,
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := withTraceBridge(shellPOSIX, "OURS", before)
			if !ok {
				t.Fatalf("stood down for %q", before)
			}
			if strings.Contains(got, foreignBridge) || strings.Contains(got, "alsonotours") {
				t.Fatalf("a foreign bridge survived: %q", got)
			}
			if !strings.HasPrefix(got, bridgeEnv+"=OURS ") || !strings.HasSuffix(got, search) {
				t.Fatalf("rewritten command: %q", got)
			}
		})
	}
}

// A command this adapter already rewrote is unwrapped, not wrapped twice.
func TestBridgeUnwrapsItsOwnPowerShellForm(t *testing.T) {
	const search = "jevlin search --stdin"
	once, _ := withTraceBridge(shellPowerShell, "FIRST", search)
	twice, ok := withTraceBridge(shellPowerShell, "SECOND", once)
	if !ok {
		t.Fatal("stood down for its own form")
	}
	if strings.Contains(twice, "FIRST") {
		t.Fatalf("the first bridge survived: %q", twice)
	}
	if strings.Count(twice, "try {") != 1 || strings.Count(twice, "Remove-Item Env:") != 1 {
		t.Fatalf("the form was wrapped twice: %q", twice)
	}
}

// What is NOT a bridge assignment: the variable named inside quoted text,
// inside the request body, or anywhere this cannot prove is a standalone
// statement. Such a command is left exactly as it was, and no lineage is
// claimed for it.
func TestBridgeLeavesWhatItCannotProveStandalone(t *testing.T) {
	for name, cmd := range map[string]string{
		"inside the request body": `jevlin search --stdin <<'JSON'` + "\n" +
			`{"version":1,"query":"what does ` + bridgeEnv + `=x do"}` + "\nJSON",
		"inside an argument": `jevlin search --stdin --note=` + bridgeEnv + `=x`,
		"mid-command":        `jevlin search --stdin ; ` + bridgeEnv + `=x`,
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := withTraceBridge(shellPOSIX, "OURS", cmd)
			if ok {
				t.Fatalf("rewrote a command carrying a bridge it could not remove:\n%s", got)
			}
			if got != cmd {
				t.Fatalf("the command was changed anyway:\n got %q\nwant %q", got, cmd)
			}
		})
	}
}

// The POSIX prefix is grammar: it binds to the first simple command and the
// shell rejects it before a compound one. A bridge is therefore written only
// when that first simple command is provably the search; every other shape
// is left byte-identical, because a search that loses its bridge still runs
// while a command broken by a prefix does not (observed live: a for-loop
// died on `V=x for`, and `cd … && jevlin search` handed the bridge to cd).
func TestThePOSIXPrefixBindsToTheSearchOrStandsDown(t *testing.T) {
	const search = "jevlin search --stdin"
	for name, cmd := range map[string]string{
		"plain":                   search,
		"leading spaces":          "   " + search,
		"quoted path":             `'/opt/tool/bin/jevlin' search -config '/tmp/a.toml' --stdin < q.json`,
		"leads a pipeline":        search + " | head -c 100",
		"leads a list":            search + " && echo done",
		"leading assignment":      "FOO=1 " + search,
		"two leading assignments": "FOO=1 BAR=x2 " + search,
	} {
		t.Run("rewritten/"+name, func(t *testing.T) {
			got, ok := withTraceBridge(shellPOSIX, "OURS", cmd)
			if !ok {
				t.Fatalf("stood down for %q", cmd)
			}
			if !strings.HasPrefix(got, bridgeEnv+"=OURS ") {
				t.Fatalf("no leading bridge: %q", got)
			}
		})
	}
	for name, cmd := range map[string]string{
		"for loop":                `for q in a b; do ` + search + `; done`,
		"while loop":              `while read -r q; do ` + search + `; done < qs.txt`,
		"if statement":            `if true; then ` + search + `; fi`,
		"subshell":                `(` + search + `)`,
		"brace group":             `{ ` + search + `; }`,
		"after cd":                `cd /tmp && ` + search,
		"second in a pipeline":    `echo '{"version":1}' | ` + search,
		"after a semicolon":       `true; ` + search,
		"quoted assignment value": `FOO='a b' ` + search,
	} {
		t.Run("left/"+name, func(t *testing.T) {
			got, ok := withTraceBridge(shellPOSIX, "OURS", cmd)
			if ok {
				t.Fatalf("rewrote a command the prefix cannot reach the search in:\n%s", got)
			}
			if got != cmd {
				t.Fatalf("the command was changed anyway:\n got %q\nwant %q", got, cmd)
			}
		})
	}
	// The PowerShell form is a statement sequence and $env: is process-wide,
	// so a compound statement keeps its bridge there.
	if _, ok := withTraceBridge(shellPowerShell, "OURS", "foreach ($q in 1,2) { "+search+" }"); !ok {
		t.Fatal("the PowerShell arm stood down for a compound statement it can carry")
	}
}

// The two leads guards are one regex, byte for byte, so the Go hook and the
// JS adapters cannot drift on WHERE a prefix is allowed to land.
func TestTheLeadsGuardsAreOneRegex(t *testing.T) {
	m := regexp.MustCompile(`SEARCH_LEADS_RE\s*=\s*/(.*?)/\n`).FindStringSubmatch(agentTraceCommonJS)
	if len(m) != 2 {
		t.Fatal("could not find SEARCH_LEADS_RE in the shared trace source")
	}
	if m[1] != posixSearchLeadsRe.String() {
		t.Fatalf("the leads guards disagree:\n  js %s\n  go %s", m[1], posixSearchLeadsRe.String())
	}
}

// Claude Code's payload names the tool, and the tool decides the syntax.
func TestBridgeShellFollowsTheToolName(t *testing.T) {
	for name, want := range map[string]shellKind{"Bash": shellPOSIX, "bash": shellPOSIX, "PowerShell": shellPowerShell, "": shellPOSIX} {
		got, ok := bridgeShellForTool(name)
		if !ok || got != want {
			t.Errorf("tool %q: %v (ok=%v), want %v", name, got, ok, want)
		}
	}
	if _, ok := bridgeShellForTool("SomeOtherShell"); ok {
		t.Error("a tool this client does not know was given a shell")
	}
}

// The Go guard and the JavaScript one are one rule. Both are run over the
// same inputs, and a disagreement is the drift this pinning prevents — the
// adapters and the hooks would otherwise recognize different things as "a
// bridge already here", which is what H-R4 turns on.
func TestBridgeGuardsAgree(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to compare the shared trace source with the Go guard")
	}
	const search = "jevlin search --stdin"
	cases := map[string]struct {
		cmd   string
		shell shellKind
	}{
		"plain posix":            {search, shellPOSIX},
		"plain powershell":       {search, shellPowerShell},
		"foreign posix bridge":   {bridgeEnv + "=" + foreignBridge + " " + search, shellPOSIX},
		"foreign env bridge":     {"$env:" + bridgeEnv + " = '" + foreignBridge + "'\n" + search, shellPowerShell},
		"foreign cmd bridge":     {"set " + bridgeEnv + "=" + foreignBridge + " && " + search, shellPOSIX},
		"two foreign bridges":    {bridgeEnv + "=a " + bridgeEnv + "=b " + search, shellPOSIX},
		"bridge inside the body": {search + " <<'JSON'\n{\"q\":\"" + bridgeEnv + "=x\"}\nJSON", shellPOSIX},
		"bridge mid-command":     {search + " ; " + bridgeEnv + "=x", shellPOSIX},
		"compound command":       {"for q in a b; do " + search + "; done", shellPOSIX},
		"search after cd":        {"cd /tmp && " + search, shellPOSIX},
		"search second in pipe":  {"echo x | " + search, shellPOSIX},
		"search leads a pipe":    {search + " | head", shellPOSIX},
		"leading assignment":     {"FOO=1 " + search, shellPOSIX},
		"powershell compound":    {"foreach ($q in 1,2) { " + search + " }", shellPowerShell},
	}
	input := map[string]map[string]string{}
	for name, c := range cases {
		input[name] = map[string]string{"cmd": c.cmd, "shell": string(c.shell)}
	}
	payload, _ := json.Marshal(map[string]any{"shared": agentTraceCommonJS, "cases": input})
	script := `
 const fs = await import('node:fs');
 const input = JSON.parse(fs.readFileSync(0,'utf8'));
 const src = input.shared + "\nexport { withTraceBridge };";
 const mod = await import('data:text/javascript;base64,'+Buffer.from(src).toString('base64'));
 const out = {};
 for (const [name, c] of Object.entries(input.cases)) {
  const got = mod.withTraceBridge(c.cmd, 'OURS', c.shell);
  out[name] = got === null ? "LEFT ALONE" : got;
 }
 process.stdout.write(JSON.stringify(out));`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", script) // #nosec G204 -- fixed test script and local Node runtime
	cmd.Stdin = strings.NewReader(string(payload))
	stdout, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running the shared source: %v\n%s", err, stdout)
	}
	var got map[string]string
	if err := json.Unmarshal(stdout, &got); err != nil {
		t.Fatalf("output %q: %v", stdout, err)
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			goCmd, ok := withTraceBridge(c.shell, "OURS", c.cmd)
			want := goCmd
			if !ok {
				want = "LEFT ALONE"
			}
			if got[name] != want {
				t.Fatalf("the two guards disagree:\n  js %q\n  go %q", got[name], want)
			}
		})
	}
}
