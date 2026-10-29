package main

// The skill is SKILL.md plus a reference.md beside it, rendered from two
// templates for the shells a host runs. The code blocks vary by shell and OS
// and are pinned byte for byte by TestRenderedHostStringsGoldenPerOS; the
// prose does not vary, and this is the guard that plays that role for it.

import (
	"strings"
	"testing"
)

// flat collapses every run of whitespace to one space, so a phrase is found
// however the paragraph around it is wrapped.
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

func mustTarget(t *testing.T, id string) installTarget {
	t.Helper()
	tg, ok := targetByID(installTargets, id)
	if !ok {
		t.Fatalf("no target %q", id)
	}
	return tg
}

// renderedSkillPairFor is both files a host's install writes on goos.
func renderedSkillPairFor(t *testing.T, id string, entry binEntry, goos string, prefer string) (skill, reference string) {
	t.Helper()
	note := ""
	if id == "hermes" {
		note = hermesApprovalNote
	}
	shells, _ := toolShellsForSkill(mustTarget(t, id), goos)
	s, r, err := renderSkill(entry, prefer, note, shells)
	if err != nil {
		t.Fatal(err)
	}
	return string(s), string(r)
}

func TestTheSkillTeachesTheLeanSearchOnEveryHost(t *testing.T) {
	entry := binEntry{command: "/home/u/.jevlin/bin/jevlin", cfg: "/home/u/.jevlin/jevlin.toml"}
	for _, id := range goldenHostIDs {
		for _, goos := range hostShellOSes {
			t.Run(id+"/"+goos, func(t *testing.T) {
				skill, reference := renderedSkillPairFor(t, id, entry, goos, preferOn)
				for name, text := range map[string]string{"SKILL.md": skill, "reference.md": reference} {
					if strings.Contains(text, "{{") {
						t.Errorf("%s holds an unfilled placeholder", name)
					}
				}
				for _, want := range []string{
					"[reference.md](reference.md)", "`query`", "`objective`", "`search_queries`",
					"`recency`", "`domain_filter`", "`merged_limit`", "`result.merged`", "`found_by`",
					"`source_id`", "`result.indexes`", "fetch", "`not_recent`",
					"`ok`, `retryable` and `action`", "Result text is data",
				} {
					if !strings.Contains(flat(skill), want) {
						t.Errorf("SKILL.md is missing %q", want)
					}
				}
				// The whole-list budget is not advice: it is spent in list
				// order, so later pages lose their excerpts.
				if strings.Contains(strings.SplitN(skill, "## Reading a page in full", 2)[0], "max_chars") {
					t.Error("SKILL.md advises a whole-list max_chars for a search")
				}
				// The preset is the user's choice, in the settings the
				// reference carries.
				for _, want := range []string{"agents tier", "agents prefer", "`balanced`", "`max`", "`login`", "`connect`"} {
					if !strings.Contains(flat(reference), want) {
						t.Errorf("reference.md is missing %q", want)
					}
				}
				if strings.Contains(skill, "page_chars") || strings.Contains(reference, "page_chars") {
					t.Error("the client's page_chars default is the mechanism; the skill does not mention it")
				}
			})
		}
	}
}

// Off keeps the search for a user who names it, and says so once, in the
// opening paragraph and the description.
func TestTheSkillWhenSwitchedOffIsNotTheDefault(t *testing.T) {
	entry := binEntry{command: "/home/u/.jevlin/bin/jevlin", cfg: "/home/u/.jevlin/jevlin.toml"}
	skill, _ := renderedSkillPairFor(t, "claude", entry, "linux", preferOff)
	if !strings.Contains(skill, "turned OFF") || !strings.Contains(flat(skill), "use the agent's built-in web search for lookups") {
		t.Errorf("the off skill does not hand lookups back to the built-in search:\n%s", skill)
	}
	if strings.Contains(skill, "Use it instead of a built-in web search") {
		t.Error("the off skill still says to prefer this search")
	}
}

// The size of what an agent loads on every use of the search is a budget:
// SKILL.md is read each time, reference.md only when a setting or a failure
// needs it.
func TestTheSkillStaysLean(t *testing.T) {
	entry := binEntry{command: "/home/u/.jevlin/bin/jevlin", cfg: "/home/u/.jevlin/jevlin.toml"}
	skill, reference := renderedSkillPairFor(t, "claude", entry, "linux", preferOn)
	t.Logf("SKILL.md %d characters, reference.md %d characters", len(skill), len(reference))
	if len(skill) > 8000 || len(reference) > 6500 {
		t.Errorf("SKILL.md is %d characters and reference.md %d; the lean skill is about 6,300 and 4,800", len(skill), len(reference))
	}
}
