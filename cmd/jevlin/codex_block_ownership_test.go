package main

// dropin-miner#82 and dropin-miner#88 item 4, driven through `agents install` and `agents uninstall`
// against the shapes Codex actually leaves behind.
//
// The damage dropin-miner#82 reports is the reason these cases exist rather than a unit
// test of the split alone: the tester's uninstall left a 0-byte
// ~/.codex/config.toml, destroying Codex's folder trust and its `[windows]
// sandbox = "unelevated"` choice, and the setup that followed restored only
// jevlin's own block. Codex's sandbox settings are what decide whether
// a search records at all, so this is the one host where losing a config
// costs a participant their earnings as well as their settings.
//
// Every fixture puts Codex's tables where Codex puts them — appended at the
// end of the file, which is INSIDE our markers whenever our block is last,
// and install made it last.

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// codexFixture is a Codex config.toml with our installed block and whatever
// the host appended, in the position named.
type codexPlacement int

const (
	insideOurBlock codexPlacement = iota // what Codex does: append to the end of the file
	afterOurBlock                        // dropin-miner#88 item 4: restored by hand below the end marker
	beforeOurBlock                       // our block is not the last thing in the file
)

const (
	codexTrust   = "[projects.'/home/u/work']\ntrust_level = \"trusted\"\n"
	codexWindows = "[windows]\nsandbox = \"unelevated\"\n"
)

// installedCodexConfig runs a real install, then places Codex's own two
// tables where the case wants them. It returns the file as it then stands.
func installedCodexConfig(t *testing.T, where codexPlacement) (m *fakeMachine, ops agentOps, cfgPath, before string) {
	t.Helper()
	cfgPath, _ = sandboxTestConfig(t)
	m, ops = newFakeMachine("codex")
	m.files["/home/u/.codex/config.toml"] = []byte("model = \"gpt-5\"\n")
	if code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install: %d\n%s%s", code, out, errOut)
	}
	installed := string(m.files["/home/u/.codex/config.toml"])
	host := codexTrust + codexWindows

	switch where {
	case insideOurBlock:
		// Codex appends to the end of the file. Our block is last, so its
		// end marker is the last line and the append lands between the
		// markers — which is the whole of dropin-miner#82.
		i := strings.LastIndex(installed, agentsMarkerEnd)
		if i < 0 {
			t.Fatalf("no end marker in the installed config:\n%s", installed)
		}
		installed = installed[:i] + host + installed[i:]
	case afterOurBlock:
		installed = strings.TrimRight(installed, "\n") + "\n\n" + host
	case beforeOurBlock:
		i := strings.Index(installed, agentsMarkerBegin)
		if i < 0 {
			t.Fatalf("no begin marker in the installed config:\n%s", installed)
		}
		installed = installed[:i] + host + "\n" + installed[i:]
	}
	m.files["/home/u/.codex/config.toml"] = []byte(installed)
	return m, ops, cfgPath, installed
}

// keptVerbatim is the assertion dropin-miner#82 is about: the host's tables are still
// there, byte for byte, and our own is not.
func keptVerbatim(t *testing.T, got string) {
	t.Helper()
	for _, want := range []string{codexTrust, codexWindows} {
		if !strings.Contains(got, want) {
			t.Errorf("Codex's own table did not survive byte-identical.\nwant to find:\n%s\ngot file:\n%s", want, got)
		}
	}
	if strings.Contains(got, agentsMarkerBegin) || strings.Contains(got, "["+codexSandboxTable+"]") {
		t.Errorf("our own block survived the uninstall:\n%s", got)
	}
	if !strings.Contains(got, "model = \"gpt-5\"") {
		t.Errorf("the participant's own setting outside the block was lost:\n%s", got)
	}
}

func TestUninstallKeepsTheTablesCodexAppendedIntoOurBlock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		where codexPlacement
	}{
		{"Codex appended them inside our markers", insideOurBlock},
		{"our block is not the last thing in the file", beforeOurBlock},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ops, cfgPath, _ := installedCodexConfig(t, tc.where)
			code, out, errOut := runAgents(t, ops, nil, "uninstall", "-config", cfgPath, "-yes")
			if code != exitOK {
				t.Fatalf("uninstall: %d\n%s%s", code, out, errOut)
			}
			keptVerbatim(t, string(m.files["/home/u/.codex/config.toml"]))
		})
	}
}

// The plan says what it is keeping and names it, so a participant reading a
// dry run knows their settings are not about to go.
func TestTheUninstallDryRunNamesTheTablesItKeeps(t *testing.T) {
	m, ops, cfgPath, before := installedCodexConfig(t, insideOurBlock)
	code, out, errOut := runAgents(t, ops, nil, "uninstall", "-config", cfgPath, "-dry-run")
	if code != exitOK {
		t.Fatalf("dry run: %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{"keeping 2 tables", "[projects.'/home/u/work']", "[windows]"} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run did not say %q:\n%s", want, out)
		}
	}
	if after := string(m.files["/home/u/.codex/config.toml"]); after != before {
		t.Errorf("the dry run changed the file:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// A file holding nothing but our block: the block goes and what is left is
// what was left before. This is the case the
// existing golden covers, pinned here so the keeping path cannot quietly
// change it.
func TestUninstallWithNothingOfCodexsInTheBlockIsUnchanged(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	if code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install: %d\n%s%s", code, out, errOut)
	}
	if code, out, errOut := runAgents(t, ops, nil, "uninstall", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("uninstall: %d\n%s%s", code, out, errOut)
	}
	if got := strings.TrimSpace(string(m.files["/home/u/.codex/config.toml"])); got != "" {
		t.Errorf("a config holding only our block did not come back empty:\n%q", got)
	}
}

// dropin-miner#88 item 4: the idempotence check compares our block's content, not the
// file's tail. With Codex's tables restored below the end marker, the block
// is unchanged and there is nothing to do.
func TestASecondInstallHasNothingToDoWhateverFollowsOurBlock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		where codexPlacement
	}{
		{"Codex's tables below our end marker", afterOurBlock},
		{"our block is not the last thing in the file", beforeOurBlock},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ops, cfgPath, before := installedCodexConfig(t, tc.where)
			code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-dry-run")
			if code != exitOK {
				t.Fatalf("dry run: %d\n%s%s", code, out, errOut)
			}
			if !strings.Contains(out, "nothing to do") {
				t.Errorf("a second install still plans a write although our block is unchanged:\n%s", out)
			}
			if after := string(m.files["/home/u/.codex/config.toml"]); after != before {
				t.Errorf("the dry run changed the file:\n%s", after)
			}
		})
	}
}

// The full round trip the plan asks for: a real uninstall, a fresh install,
// then a dry run that has nothing to do. This is what the tester did by
// hand, and where they found install planning a write forever.
func TestUninstallThenInstallThenADryRunHasNothingToDo(t *testing.T) {
	m, ops, cfgPath, _ := installedCodexConfig(t, insideOurBlock)

	if code, out, errOut := runAgents(t, ops, nil, "uninstall", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("uninstall: %d\n%s%s", code, out, errOut)
	}
	keptVerbatim(t, string(m.files["/home/u/.codex/config.toml"]))

	if code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("reinstall: %d\n%s%s", code, out, errOut)
	}
	restored := string(m.files["/home/u/.codex/config.toml"])
	for _, want := range []string{codexTrust, codexWindows, agentsMarkerBegin} {
		if !strings.Contains(restored, want) {
			t.Fatalf("after reinstall, %q is missing:\n%s", want, restored)
		}
	}

	code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-dry-run")
	if code != exitOK {
		t.Fatalf("dry run: %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "nothing to do") {
		t.Errorf("the dry run after a fresh install still plans a write:\n%s", out)
	}
	if after := string(m.files["/home/u/.codex/config.toml"]); after != restored {
		t.Errorf("the dry run changed the file:\n%s", after)
	}
}

// Install moves a table Codex appended into our block out below it, so the
// host's next append lands outside ours and cannot be swept up again. The
// same byte-range delete was on the install path too — the issue reported
// only uninstall because that is where the tester met it.
func TestInstallMovesCodexsTablesOutOfOurBlockRatherThanDeletingThem(t *testing.T) {
	m, ops, cfgPath, _ := installedCodexConfig(t, insideOurBlock)

	code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes")
	if code != exitOK {
		t.Fatalf("install: %d\n%s%s", code, out, errOut)
	}
	got := string(m.files["/home/u/.codex/config.toml"])
	for _, want := range []string{codexTrust, codexWindows} {
		if !strings.Contains(got, want) {
			t.Fatalf("install deleted a table Codex had appended into our block:\n%s", got)
		}
	}
	end := strings.Index(got, agentsMarkerEnd)
	if end < 0 {
		t.Fatalf("our block is gone:\n%s", got)
	}
	for _, want := range []string{codexTrust, codexWindows} {
		if strings.Index(got, want) < end {
			t.Errorf("%q is still inside our markers, where the next append would join it:\n%s", want, got)
		}
	}
	if !strings.Contains(out, "moving 2 tables") {
		t.Errorf("the plan did not say it was moving them:\n%s", out)
	}
}

// A block whose tables cannot be read is left exactly as it is, and said so
// — the refuse-rather-than-guess rule the installer uses everywhere else.
// Deleting a region this client cannot parse is how a participant's config
// gets destroyed, which is the whole of dropin-miner#82.
func TestABlockThatCannotBeReadIsLeftAloneAndReported(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	if code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install: %d\n%s%s", code, out, errOut)
	}
	installed := string(m.files["/home/u/.codex/config.toml"])
	// A table header spelled inside a multi-line string: a line scan that
	// cut there would leave an unterminated string on both sides.
	i := strings.LastIndex(installed, agentsMarkerEnd)
	broken := installed[:i] + "[notes]\ntext = \"\"\"\n[windows]\nnot a table\n\"\"\"\n" + installed[i:]
	m.files["/home/u/.codex/config.toml"] = []byte(broken)

	code, out, errOut := runAgents(t, ops, nil, "uninstall", "-config", cfgPath, "-yes")
	if code != exitOK {
		t.Fatalf("uninstall: %d\n%s%s", code, out, errOut)
	}
	if got := string(m.files["/home/u/.codex/config.toml"]); got != broken {
		t.Errorf("a block that could not be read was changed anyway:\n%s", got)
	}
	if !strings.Contains(out, "cannot be read as TOML tables") {
		t.Errorf("the refusal was not reported:\n%s", out)
	}
}

// H5's attribution still decides first: another installation's block is left
// alone whatever is inside it, so this commit cannot have widened what
// uninstall is willing to touch.
func TestAnotherInstallationsBlockIsStillLeftAloneWithHostTablesInIt(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	other := filepath.Join(t.TempDir(), "other-install")
	otherRoots := []string{filepath.Join(other, "state")}
	block := string(codexSandboxBlock(otherRoots))
	i := strings.LastIndex(block, agentsMarkerEnd)
	seeded := "model = \"gpt-5\"\n\n" + block[:i] + codexWindows + block[i:]
	m.files["/home/u/.codex/config.toml"] = []byte(seeded)

	code, out, errOut := runAgents(t, ops, nil, "uninstall", "-config", cfgPath, "-yes")
	if code != exitOK {
		t.Fatalf("uninstall: %d\n%s%s", code, out, errOut)
	}
	if got := string(m.files["/home/u/.codex/config.toml"]); got != seeded {
		t.Errorf("another installation's block was touched:\n--- before ---\n%s\n--- after ---\n%s", seeded, got)
	}
	// The reason names the owner, in the words a left skill is named in
	// (dropin-miner#128). Taken from the production reading rather than typed: the
	// sentence spells the home the roots lie under, and a typed copy of it
	// would pass while production named something else.
	if want := belongsTo(describeSandboxOwner(otherRoots)); !strings.Contains(out, want) {
		t.Errorf("the reason was not reported\nwant to find: %s\ngot:\n%s", want, out)
	}
}

// ── L2b: a header the first pattern could not see ───────────────────────

// Codex keys a project's trust by its path, so a folder named `work [1]`
// puts a `]` inside a quoted key. The first header pattern said "anything
// but ]" between the brackets, could not match these, and so saw no
// boundary: the table merged into our section and was deleted with it, exit
// 0, no note. Reproduced through both real paths before this was written.
var bracketedHeaders = []struct{ name, table string }{
	{"a ] inside a single-quoted key", "[projects.'/home/u/work [1]']\ntrust_level = \"trusted\"\n"},
	{"a ] inside a double-quoted key", "[projects.\"/home/u/a]b\"]\ntrust_level = \"trusted\"\n"},
	{"an escaped quote before the ]", "[projects.\"/home/u/say \\\"hi\\\" ]x\"]\ntrust_level = \"trusted\"\n"},
}

// insideOurMarkers runs a real install and then puts text where Codex puts
// its appends: at the end of the file, which is inside our markers.
func insideOurMarkers(t *testing.T, text string) (m *fakeMachine, ops agentOps, cfgPath, seeded string) {
	t.Helper()
	cfgPath, _ = sandboxTestConfig(t)
	m, ops = newFakeMachine("codex")
	m.files["/home/u/.codex/config.toml"] = []byte("model = \"gpt-5\"\n")
	if code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install: %d\n%s%s", code, out, errOut)
	}
	installed := string(m.files["/home/u/.codex/config.toml"])
	i := strings.LastIndex(installed, agentsMarkerEnd)
	if i < 0 {
		t.Fatalf("no end marker in the installed config:\n%s", installed)
	}
	seeded = installed[:i] + text + installed[i:]
	m.files["/home/u/.codex/config.toml"] = []byte(seeded)
	return m, ops, cfgPath, seeded
}

// Kept AND removed, not merely refused: a refusal also leaves the table in
// the file, so asserting only that it survived would pass on the net alone
// and leave the grammar unpinned.
func TestUninstallKeepsATableWhoseHeaderHoldsABracket(t *testing.T) {
	for _, tc := range bracketedHeaders {
		t.Run(tc.name, func(t *testing.T) {
			m, ops, cfgPath, _ := insideOurMarkers(t, tc.table)
			code, out, errOut := runAgents(t, ops, nil, "uninstall", "-config", cfgPath, "-yes")
			if code != exitOK {
				t.Fatalf("uninstall: %d\n%s%s", code, out, errOut)
			}
			got := string(m.files["/home/u/.codex/config.toml"])
			if !strings.Contains(got, tc.table) {
				t.Fatalf("the table was destroyed:\n%s", got)
			}
			if strings.Contains(got, agentsMarkerBegin) || strings.Contains(got, "["+codexSandboxTable+"]") {
				t.Errorf("our own table was not removed — the header was not recognized as a boundary, and only the net saved the table:\n%s\n%s", got, out)
			}
			if !strings.Contains(out, "keeping 1 table") {
				t.Errorf("the plan did not name what it kept:\n%s", out)
			}
		})
	}
}

func TestInstallKeepsATableWhoseHeaderHoldsABracket(t *testing.T) {
	for _, tc := range bracketedHeaders {
		t.Run(tc.name, func(t *testing.T) {
			m, ops, cfgPath, _ := insideOurMarkers(t, tc.table)
			code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes")
			if code != exitOK {
				t.Fatalf("install: %d\n%s%s", code, out, errOut)
			}
			got := string(m.files["/home/u/.codex/config.toml"])
			if !strings.Contains(got, tc.table) {
				t.Fatalf("the table was destroyed:\n%s", got)
			}
			end := strings.Index(got, agentsMarkerEnd)
			if end < 0 || strings.Index(got, tc.table) < end {
				t.Errorf("the table was not moved out below our block — the header was not recognized as a boundary, and only the net saved the table:\n%s\n%s", got, out)
			}
		})
	}
}

// The net, handed directly the thing it exists to stop: a section classified
// as ours that is carrying somebody else's table. No header trick is needed
// to build it, which is the point — the net must not depend on knowing which
// header the scan will miss next.
func TestTheNetRefusesASectionThatIsNotOnlyOurs(t *testing.T) {
	ours := sandboxSettings([]string{"/home/u/.jevlin/state"})
	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		{"our table alone", ours, true},
		{"our table with our comments above it", "# a comment\n" + ours, true},
		{"our table and a foreign one", ours + "[windows]\nsandbox = \"unelevated\"\n", false},
		{"our table and the table the first pattern missed", ours + "[projects.'/home/u/work [1]']\ntrust_level = \"trusted\"\n", false},
		{"a sub-table nested under our own name", ours + "[" + codexSandboxTable + ".'a]b']\nk = 1\n", false},
		// What Codex writes on approving a hook (issue #19), riding in our section.
		{"our table and Codex's record of a hook approval", ours + "\n[hooks.state.\"/home/u/.codex/hooks.json:pre_tool_use:0:0\"]\ntrusted_hash = \"sha256:00\"\n", false},
		{"an inline table inside ours", ours + "extra = { k = 1 }\n", false},
		{"a foreign table alone", "[windows]\nsandbox = \"unelevated\"\n", false},
		{"not TOML", "= = =\n", false},
		{"nothing at all", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := oursIsOnlyOurs(tc.text); got != tc.want {
				t.Fatalf("oursIsOnlyOurs = %v, want %v, for:\n%s", got, tc.want, tc.text)
			}
		})
	}
}

// The net's other direction, through both real paths. A sub-table written
// under our own name is recognized as a header and classified as somebody
// else's — correctly, the renderer never writes it — but keeping it while
// removing or re-rendering our table would leave Codex a config that defines
// [sandbox_workspace_write] twice, or a child with no parent it was written
// for. Neither path may guess, so both leave the block exactly as it is and
// say so.
func TestABlockHoldingATableUnderOurNameIsLeftAlone(t *testing.T) {
	nested := "[" + codexSandboxTable + ".mine]\nk = 1\n"
	m, ops, cfgPath, seeded := insideOurMarkers(t, nested)
	for _, verb := range []string{"uninstall", "install"} {
		code, out, errOut := runAgents(t, ops, nil, verb, "-config", cfgPath, "-yes")
		if code != exitOK && verb == "uninstall" {
			t.Fatalf("%s: %d\n%s%s", verb, code, out, errOut)
		}
		if got := string(m.files["/home/u/.codex/config.toml"]); got != seeded {
			t.Fatalf("%s changed a block it could not attribute:\n%s", verb, got)
		}
		if !strings.Contains(out+errOut, "cannot be read as TOML tables") {
			t.Errorf("%s did not say why it left the block:\n%s%s", verb, out, errOut)
		}
	}
}

// A key the participant added inside OUR table goes with the table — the
// table between our markers is ours to render — and is named in the plan
// first, on both paths, never dropped silently.
func TestAKeyAddedInsideOurTableIsNamedBeforeItGoes(t *testing.T) {
	for _, verb := range []string{"uninstall", "install"} {
		t.Run(verb, func(t *testing.T) {
			_, ops, cfgPath, _ := insideOurMarkers(t, "exclude_slash_tmp = true\n")
			code, out, errOut := runAgents(t, ops, nil, verb, "-config", cfgPath, "-dry-run")
			if code != exitOK {
				t.Fatalf("%s -dry-run: %d\n%s%s", verb, code, out, errOut)
			}
			for _, want := range []string{"exclude_slash_tmp", "did not write", "goes with the table"} {
				if !strings.Contains(out, want) {
					t.Errorf("the plan did not say %q:\n%s", want, out)
				}
			}
		})
	}
}

// ── issue #19: Codex's record of a hook approval ────────────────────────
//
// When a participant approves a hook, Codex writes one table per hook to
// config.toml: [hooks.state."<hooks.json>:<event>:<i>:<j>"], holding a
// trusted_hash. It writes them at the end of the file, so with our block last
// they land inside our markers — on macOS 0.160.0 all eleven did. They are
// Codex's, not ours, and the participant's approval is in them: losing one
// silently turns a hook off until it is approved again.
//
// The shapes come from config.toml captures (codex_fixtures_test.go): the
// macOS block, re-marked and DERIVED from the capture as that file says, and
// the Linux file whole, whose bare [hooks.state] parent table is a shape of
// its own.

const macosAfterTrustBlock = "config-0.160.0-macos.after-trust.block.toml"

// macosHookTrust is what Codex 0.160.0 wrote inside our block on approval:
// from the end of our last key to the end marker, byte for byte.
func macosHookTrust(t *testing.T) string {
	t.Helper()
	_, region, _, ok := markedRegion([]byte(codexConfigFixture(t, macosAfterTrustBlock)))
	if !ok {
		t.Fatalf("%s holds no marked block", macosAfterTrustBlock)
	}
	i := strings.Index(region, "\n\n[hooks.state.")
	if i < 0 {
		t.Fatalf("%s holds no hook approval after our last key", macosAfterTrustBlock)
	}
	return region[i+1:]
}

// linuxHookTrust is what Codex 0.158.0 wrote on Linux: the bare parent table
// and nine approvals, from the first [hooks.state] line to the end.
func linuxHookTrust(t *testing.T) string {
	t.Helper()
	text := codexConfigFixture(t, "config-0.158.0-linux.after-trust.toml")
	i := strings.Index(text, "[hooks.state]\n")
	if i < 0 {
		t.Fatal("the Linux capture holds no bare [hooks.state] table")
	}
	return text[i:]
}

// hookTrustOf is the approvals a config records, decoded: the assertion is
// about what Codex will read back, not about where the bytes sit.
func hookTrustOf(t *testing.T, text string) map[string]any {
	t.Helper()
	var doc struct {
		Hooks struct {
			State map[string]any `toml:"state"`
		} `toml:"hooks"`
	}
	if _, err := toml.Decode(text, &doc); err != nil {
		t.Fatalf("the config does not decode: %v\n%s", err, text)
	}
	return doc.Hooks.State
}

// codexHookTrustShapes are the captured shapes, each placed where Codex puts
// it: at the end of the file, inside our markers.
func codexHookTrustShapes(t *testing.T) []struct {
	name   string
	text   string
	tables int
} {
	t.Helper()
	return []struct {
		name   string
		text   string
		tables int
	}{
		{"macOS 0.160.0, eleven approvals", macosHookTrust(t), 11},
		{"Linux 0.158.0, the bare parent and nine approvals", linuxHookTrust(t), 10},
	}
}

func TestUninstallKeepsCodexsHookTrustInsideOurBlock(t *testing.T) {
	for _, shape := range codexHookTrustShapes(t) {
		t.Run(shape.name, func(t *testing.T) {
			m, ops, cfgPath, seeded := insideOurMarkers(t, shape.text)
			want := hookTrustOf(t, seeded)
			if len(want) == 0 {
				t.Fatal("the seeded config records no approval, so keeping it proves nothing")
			}
			code, out, errOut := runAgents(t, ops, nil, "uninstall", "-config", cfgPath, "-yes")
			if code != exitOK {
				t.Fatalf("uninstall: %d\n%s%s", code, out, errOut)
			}
			got := string(m.files[codexConfigPath])
			if strings.Contains(got, agentsMarkerBegin) || strings.Contains(got, "["+codexSandboxTable+"]") {
				t.Errorf("our own block survived the uninstall:\n%s", got)
			}
			if !reflect.DeepEqual(hookTrustOf(t, got), want) {
				t.Errorf("Codex's record of the approvals changed\n got %v\nwant %v", hookTrustOf(t, got), want)
			}
			if !strings.Contains(got, strings.TrimLeft(shape.text, "\n")) {
				t.Errorf("the approval tables did not survive byte for byte:\n%s", got)
			}
			if want := fmt.Sprintf("keeping %d tables", shape.tables); !strings.Contains(out, want) {
				t.Errorf("the plan did not say %q:\n%s", want, out)
			}
		})
	}
}

func TestInstallMovesCodexsHookTrustOutOfOurBlockRatherThanDeletingIt(t *testing.T) {
	for _, shape := range codexHookTrustShapes(t) {
		t.Run(shape.name, func(t *testing.T) {
			m, ops, cfgPath, seeded := insideOurMarkers(t, shape.text)
			want := hookTrustOf(t, seeded)
			code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes")
			if code != exitOK {
				t.Fatalf("install: %d\n%s%s", code, out, errOut)
			}
			got := string(m.files[codexConfigPath])
			if !reflect.DeepEqual(hookTrustOf(t, got), want) {
				t.Errorf("Codex's record of the approvals changed\n got %v\nwant %v", hookTrustOf(t, got), want)
			}
			_, region, post, ok := markedRegion([]byte(got))
			if !ok {
				t.Fatalf("our block is gone:\n%s", got)
			}
			if strings.Contains(region, "[hooks.state") {
				t.Errorf("an approval is still inside our markers, where the next refresh would have to judge it again:\n%s", got)
			}
			if !strings.Contains(post, strings.TrimLeft(shape.text, "\n")) {
				t.Errorf("the approval tables are not directly below our block, byte for byte:\n%s", got)
			}
			if want := fmt.Sprintf("moving %d tables", shape.tables); !strings.Contains(out, want) {
				t.Errorf("the plan did not say %q:\n%s", want, out)
			}
		})
	}
}

// Codex keys an approval by the path of its hooks.json, so on Windows the
// key holds backslashes: escaped in a double-quoted key, bare in a
// single-quoted one. No Windows approval has been captured. These two keys
// are DERIVED from the captured ones by TOML's key grammar alone, and the
// case says so rather than passing for a measurement.
func TestCodexsHookTrustSpelledForWindowsSurvivesBothPaths(t *testing.T) {
	basic := "\n[hooks.state.\"C:\\\\Users\\\\u\\\\.codex\\\\hooks.json:pre_tool_use:0:0\"]\ntrusted_hash = \"sha256:4dec8e969f5df1b9dcb7186eaa477ca862461e22ac63fe482807bbde0e1f4e22\"\n"
	literal := "\n[hooks.state.'C:\\Users\\u\\.codex\\hooks.json:stop:0:0']\ntrusted_hash = \"sha256:61e8fcd19218c4e9e3fac8f44747a47370e4bc9eba2aebc3e3659b99d889cea0\"\n"
	for _, verb := range []string{"uninstall", "install"} {
		t.Run(verb, func(t *testing.T) {
			m, ops, cfgPath, seeded := insideOurMarkers(t, basic+literal)
			want := hookTrustOf(t, seeded)
			for _, key := range []string{`C:\Users\u\.codex\hooks.json:pre_tool_use:0:0`, `C:\Users\u\.codex\hooks.json:stop:0:0`} {
				if _, ok := want[key]; !ok {
					t.Fatalf("the derived key does not decode to %q: %v", key, want)
				}
			}
			code, out, errOut := runAgents(t, ops, nil, verb, "-config", cfgPath, "-yes")
			if code != exitOK {
				t.Fatalf("%s: %d\n%s%s", verb, code, out, errOut)
			}
			got := string(m.files[codexConfigPath])
			if !reflect.DeepEqual(hookTrustOf(t, got), want) {
				t.Errorf("Codex's record of the approvals changed\n got %v\nwant %v", hookTrustOf(t, got), want)
			}
			for _, table := range []string{basic, literal} {
				if !strings.Contains(got, strings.TrimLeft(table, "\n")) {
					t.Errorf("a Windows-spelled approval did not survive byte for byte:\n%s", got)
				}
			}
		})
	}
}
