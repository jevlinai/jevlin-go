package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
  out[shell] = { rendered, scrubbed: m.scrubTraceText(rendered, []) };
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
