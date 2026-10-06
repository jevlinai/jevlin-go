package main

// splitMarkedBlock on its own, before anything is wired to it.
//
// This is the function dropin-miner#82 turns on: which tables inside our marked block
// did this client write, and which did Codex append into it. Getting it
// wrong in the keeping direction leaves our block behind; getting it wrong
// in the removing direction destroys a participant's Codex folder trust and
// sandbox mode, which is the damage the issue reports. So it is tested as a
// unit first, against the shapes the issue and the host actually produce,
// and only then relied on by the plan.

import (
	"slices"
	"strings"
	"testing"
)

// ourBlockRegion is the text between the markers as our own renderer writes
// it, taken from the renderer so the fixture cannot drift from production.
func ourBlockRegion(t *testing.T, roots ...string) string {
	t.Helper()
	_, region, _, ok := markedRegion(codexSandboxBlock(roots))
	if !ok {
		t.Fatalf("codexSandboxBlock does not produce a marked block:\n%s", codexSandboxBlock(roots))
	}
	return region
}

func sectionNames(sections []tomlSection) []string {
	out := make([]string, 0, len(sections))
	for _, s := range sections {
		out = append(out, s.header)
	}
	return out
}

func TestSplitMarkedBlockFindsOurOwnTableAndNothingElse(t *testing.T) {
	preamble, sections, ok := splitMarkedBlock(ourBlockRegion(t, "/home/u/.jevlin/state"))
	if !ok {
		t.Fatal("our own block did not split")
	}
	if got := sectionNames(sections); len(got) != 1 || got[0] != codexSandboxTable {
		t.Fatalf("sections = %v, want exactly [%s]", got, codexSandboxTable)
	}
	// Our three comment lines are written immediately above our header, so
	// they belong to our section and the preamble is blank. If they drifted
	// into the preamble, removal would leave them behind.
	if strings.TrimSpace(preamble) != "" {
		t.Fatalf("preamble = %q, want only whitespace: our comments belong to our table", preamble)
	}
	if !strings.Contains(sections[0].text, "# Lets jevlin's search") {
		t.Fatalf("our comments are not in our section:\n%s", sections[0].text)
	}
}

// The shape dropin-miner#82 reports: Codex's own tables inside our markers.
func TestSplitMarkedBlockSeparatesCodexsOwnTables(t *testing.T) {
	region := ourBlockRegion(t, "/home/u/.jevlin/state") +
		"[projects.'/home/u/work']\ntrust_level = \"trusted\"\n" +
		"[windows]\nsandbox = \"unelevated\"\n"

	preamble, sections, ok := splitMarkedBlock(region)
	if !ok {
		t.Fatal("the reported shape did not split")
	}
	if strings.TrimSpace(preamble) != "" {
		t.Fatalf("preamble = %q, want only whitespace", preamble)
	}
	want := []string{codexSandboxTable, "projects.'/home/u/work'", "windows"}
	got := sectionNames(sections)
	if len(got) != len(want) {
		t.Fatalf("sections = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sections = %v, want %v", got, want)
		}
	}
	// Byte-identical is the promise: "kept" must mean the participant's own
	// bytes, not a re-rendering of them.
	if sections[1].text != "[projects.'/home/u/work']\ntrust_level = \"trusted\"\n" {
		t.Fatalf("the kept table was not preserved verbatim: %q", sections[1].text)
	}
	if sections[2].text != "[windows]\nsandbox = \"unelevated\"\n" {
		t.Fatalf("the kept table was not preserved verbatim: %q", sections[2].text)
	}
}

// A comment above a foreign table is that table's, not ours. Removing our
// section must not take a participant's note about their own setting.
func TestSplitMarkedBlockGivesACommentToTheTableBelowIt(t *testing.T) {
	region := ourBlockRegion(t, "/home/u/.jevlin/state") +
		"\n# I set this by hand so the non-admin sandbox works.\n[windows]\nsandbox = \"unelevated\"\n"

	_, sections, ok := splitMarkedBlock(region)
	if !ok {
		t.Fatal("did not split")
	}
	if len(sections) != 2 {
		t.Fatalf("sections = %v, want two", sectionNames(sections))
	}
	if strings.Contains(sections[0].text, "I set this by hand") {
		t.Fatalf("our section swallowed the comment above the foreign table:\n%s", sections[0].text)
	}
	if !strings.Contains(sections[1].text, "I set this by hand") {
		t.Fatalf("the foreign table lost the comment written above it:\n%s", sections[1].text)
	}
}

// The one way a line scan can be fooled: a table header spelled inside a
// multi-line string. Cutting there leaves an unterminated string on both
// sides, which is what the decode check is for. ok=false means the caller
// leaves the whole block alone rather than act on a bad reading.
func TestSplitMarkedBlockRefusesWhatItCannotCutCleanly(t *testing.T) {
	region := ourBlockRegion(t, "/home/u/.jevlin/state") +
		"[notes]\ntext = \"\"\"\n[windows]\nnot a table at all\n\"\"\"\n"

	if _, _, ok := splitMarkedBlock(region); ok {
		t.Fatal("split a region whose sections cannot stand on their own; the caller would act on a bad reading")
	}
}

func TestSplitMarkedBlockRefusesARegionThatIsNotTOML(t *testing.T) {
	if _, _, ok := splitMarkedBlock("this is not = = toml\n"); ok {
		t.Fatal("split a region that is not TOML")
	}
}

// A block with no table at all is all preamble, and still has to be readable
// as TOML for anything to be concluded from it.
func TestSplitMarkedBlockWithNoTableIsAllPreamble(t *testing.T) {
	preamble, sections, ok := splitMarkedBlock("\n# only a comment\n")
	if !ok {
		t.Fatal("a comment-only region did not split")
	}
	if len(sections) != 0 {
		t.Fatalf("sections = %v, want none", sectionNames(sections))
	}
	if !strings.Contains(preamble, "only a comment") {
		t.Fatalf("preamble = %q", preamble)
	}
}

// Two headers back to back: the second must not steal the first's header
// line as its own attached-comment run.
func TestSplitMarkedBlockKeepsAdjacentHeadersApart(t *testing.T) {
	_, sections, ok := splitMarkedBlock("[a]\n[b]\nk = 1\n")
	if !ok {
		t.Fatal("did not split")
	}
	if got := sectionNames(sections); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("sections = %v, want [a b]", got)
	}
	if sections[0].text != "[a]\n" {
		t.Fatalf("section a = %q, want just its header line", sections[0].text)
	}
}

// An array-of-tables header is a table header too.
func TestSplitMarkedBlockRecognizesAnArrayOfTables(t *testing.T) {
	_, sections, ok := splitMarkedBlock("[[provider]]\nname = \"x\"\n")
	if !ok {
		t.Fatal("did not split")
	}
	if got := sectionNames(sections); len(got) != 1 || got[0] != "provider" {
		t.Fatalf("sections = %v, want [provider]", got)
	}
}

// TOML's key grammar, not "anything but ]": a quoted segment may hold any
// bracket it likes. Each of these was no boundary at all to the first
// pattern, so the table merged into the section above it.
func TestSplitMarkedBlockReadsAHeaderByTheKeyGrammar(t *testing.T) {
	for _, header := range []string{
		`projects.'/home/u/work [1]'`,
		`projects."/home/u/a]b"`,
		`projects."/home/u/say \"hi\" ]x"`,
		`a . 'b]' . "c]"`,
		`'only]quoted'`,
		// Codex's record of hook approvals (issue #19): the bare parent Linux
		// 0.158.0 writes, one captured key, and the two Windows spellings of a
		// key, which are derived from the grammar rather than captured.
		`hooks.state`,
		`hooks.state."/home/u/.codex/hooks.json:pre_tool_use:0:0"`,
		`hooks.state."C:\\Users\\u\\.codex\\hooks.json:stop:0:0"`,
		`hooks.state.'C:\Users\u\.codex\hooks.json:stop:0:0'`,
	} {
		t.Run(header, func(t *testing.T) {
			region := ourBlockRegion(t, "/home/u/.jevlin/state") + "[" + header + "]\nk = 1\n"
			_, sections, ok := splitMarkedBlock(region)
			if !ok {
				t.Fatal("did not split")
			}
			if len(sections) != 2 {
				t.Fatalf("sections = %v, want ours and [%s]: the header was not a boundary", sectionNames(sections), header)
			}
			if sections[1].header != header {
				t.Fatalf("header = %q, want %q", sections[1].header, header)
			}
			if strings.Contains(sections[0].text, "k = 1") {
				t.Fatalf("our section swallowed the table below it:\n%s", sections[0].text)
			}
		})
	}
}

// And what is not a header stays not one.
func TestSplitMarkedBlockDoesNotTakeAValueForAHeader(t *testing.T) {
	_, sections, ok := splitMarkedBlock("[a]\nlist = [\n  1,\n]\nother = [ \"x]\" ]\n")
	if !ok {
		t.Fatal("did not split")
	}
	if got := sectionNames(sections); len(got) != 1 || got[0] != "a" {
		t.Fatalf("sections = %v, want just [a]", got)
	}
}

// What Codex 0.160.0 left inside our markers on macOS once a participant
// approved its hooks (issue #19): eleven [hooks.state."<hooks.json>:<event>:
// <i>:<j>"] tables, after our last key, because the block was last in the
// file. Each key is one double-quoted segment holding slashes, dots and
// colons. Every table must be a section of its own, named exactly, carrying
// exactly its own bytes — the blank line Codex wrote above it included — so
// that what is kept is the participant's approval and not a re-rendering of
// it. The fixture is derived from the capture; codex_fixtures_test.go says
// how.
func TestSplitMarkedBlockSeparatesCodexsHookTrustTables(t *testing.T) {
	_, region, _, ok := markedRegion([]byte(codexConfigFixture(t, macosAfterTrustBlock)))
	if !ok {
		t.Fatal("the fixture holds no marked block")
	}
	preamble, sections, ok := splitMarkedBlock(region)
	if !ok {
		t.Fatal("the block Codex left did not split")
	}
	if strings.TrimSpace(preamble) != "" {
		t.Fatalf("preamble = %q, want only whitespace", preamble)
	}
	want := []string{codexSandboxTable}
	for _, event := range []string{"pre_tool_use", "permission_request", "post_tool_use", "pre_compact", "post_compact",
		"session_start", "session_end", "user_prompt_submit", "subagent_start", "subagent_stop", "stop"} {
		want = append(want, `hooks.state."/Users/u/.codex/hooks.json:`+event+`:0:0"`)
	}
	if got := sectionNames(sections); !slices.Equal(got, want) {
		t.Fatalf("sections =\n%q\nwant\n%q", got, want)
	}
	if !strings.HasSuffix(sections[0].text, "/state\"]\n") {
		t.Errorf("our section does not end at our last key:\n%s", sections[0].text)
	}
	var foreign strings.Builder
	for _, s := range sections[1:] {
		if !strings.HasPrefix(s.text, "\n[hooks.state.") || strings.Count(s.text, "\n[") != 1 {
			t.Errorf("a section is not exactly one approval with its blank line: %q", s.text)
		}
		foreign.WriteString(s.text)
	}
	if foreign.String() != macosHookTrust(t) {
		t.Errorf("the approvals, taken together, are not the bytes Codex wrote:\n%s", foreign.String())
	}
}
