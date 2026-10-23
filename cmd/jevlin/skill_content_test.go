package main

// S4: the skill teaches what S1-S3 made true.
//
// The prose sections skill.md carries (Tiers, Request options, Reading the
// answer) do not vary by shell or by OS — only the code blocks inside
// {{CALL}}, {{PREFER}} and {{SEARCH}} do, and those are already pinned
// byte-for-byte per OS by TestRenderedHostStringsGoldenPerOS. There is no
// byte golden for the prose itself; this is the guard that plays that role
// for it — a positive-content check, run for every installed host on every
// OS, so a paragraph quietly dropped from skill.md is caught the same way
// a dropped code block would be.

import (
	"strings"
	"testing"
)

// skillSection extracts the text between a heading and the next one (or
// end of file), heading lines excluded, for a paragraph-scoped assertion
// ("this word belongs in Tiers, not anywhere in the whole document").
func skillSection(skill, heading, nextHeading string) (string, bool) {
	start := strings.Index(skill, heading)
	if start < 0 {
		return "", false
	}
	start += len(heading)
	end := len(skill)
	if nextHeading != "" {
		if i := strings.Index(skill[start:], nextHeading); i >= 0 {
			end = start + i
		}
	}
	return skill[start:end], true
}

// flat collapses every run of whitespace to one space, so a phrase is found
// however the paragraph around it is wrapped.
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestSkillTeachesTiersOptionsAndMergedList(t *testing.T) {
	entry := binEntry{command: "/home/u/.jevlin/bin/jevlin", cfg: "/home/u/.jevlin/jevlin.toml"}
	for _, id := range goldenHostIDs {
		for _, goos := range hostShellOSes {
			t.Run(id+"/"+goos, func(t *testing.T) {
				skill := renderedSkillFor(id, entry, goos)

				// Tiers: fast is named as the default, on need alone.
				for _, want := range []string{
					"unset for a routine lookup", `"tier":"balanced"`, "several sources",
				} {
					if !strings.Contains(skill, want) {
						t.Errorf("skill is missing tier guidance %q", want)
					}
				}
				tiers, ok := skillSection(skill, "## Tiers", "## Request options")
				if !ok {
					t.Fatal("no ## Tiers section found")
				}
				for _, unwanted := range []string{"cost", "credit", "usage"} {
					if strings.Contains(tiers, unwanted) {
						t.Errorf("the Tiers paragraph mentions %q, which S4 says it must not: %s", unwanted, tiers)
					}
				}

				// Options: each named, one example JSON shape each.
				for _, want := range []string{
					"`recency`", `"recency":"month"`,
					"`domain_filter`", `"domain_filter":["developer.mozilla.org"]`,
					"`max_results`", `"max_results":3`,
					"`merged_limit`", `"merged_limit":24`, "first pass looks thin",
					"`max_chars`", `"max_chars":3000`,
				} {
					if !strings.Contains(skill, want) {
						t.Errorf("skill is missing request-option guidance %q", want)
					}
				}

				// Options: what the router actually promises (dropin-miner#131). An
				// agent that reads domain_filter as a restriction presents
				// off-host results as the named site's, or reports a
				// failure that is not one — which is what the 0.2.12
				// release check watched it do. A router with Jevlin-core #68
				// holds every page to the filter and says so in
				// result.filters_applied; an older one may not have applied
				// it, and then the agent checks each page itself. Asserted
				// inside the section, because the warning said anywhere else
				// in the document would not be read at the point the field
				// is chosen.
				options, ok := skillSection(skill, "## Request options", "## What comes back")
				if !ok {
					t.Fatal("no ## Request options section found")
				}
				for _, want := range []string{
					"When `result.filters_applied` is present, the router held every page",
					"`dropped` says how many pages it removed",
					"absent on a request that carried a filter, the router is older",
					"other hosts can still come back", "check each page's `published`",
					"`max_results` is a cap",
				} {
					if !strings.Contains(flat(options), want) {
						t.Errorf("the Request options section does not say what the router promises: missing %q\n%s", want, options)
					}
				}

				// Reading the answer: merged first, found_by as evidence,
				// view:full only for answer texts, decision, and cost
				// mentioned only if asked.
				for _, want := range []string{
					"result.merged", "found_by", `"view":"full"`,
					"decision", "usage.cost_micros", "only if the user asks",
				} {
					if !strings.Contains(skill, want) {
						t.Errorf("skill is missing reading-the-answer guidance %q", want)
					}
				}
				// The full view is what costs: it repeats each page's text
				// with every provider's candidates, so it is asked for only
				// for the answer texts, and it is not the default. And the
				// signals a degraded search sends are read as degraded, not
				// as an empty web: an index's status, deadline_hit, a page's
				// date, corroboration and the identifiers it contains.
				reading, ok := skillSection(skill, "## Reading the answer", "## Mining is not search")
				if !ok {
					t.Fatal("no ## Reading the answer section found")
				}
				for _, want := range []string{
					"answer texts", "not the default",
					"several indexes agree on are more trustworthy",
					"`matched_terms`", "`published`", "time-bound question", "`result_id`",
					"`result.indexes`", "`usage.deadline_hit`", "degraded",
					"rather than conclude that nothing exists",
					"`result.merged_total`", "more pages exist", "raise `merged_limit`",
				} {
					if !strings.Contains(flat(reading), want) {
						t.Errorf("the Reading the answer section is missing %q\n%s", want, reading)
					}
				}
				// What was untrue is gone, however the lines are wrapped.
				for _, unwanted := range []string{
					"about a second", "passes them to its", "Ask `\"view\":\"merged\"` when tokens matter",
					// #132's sentence for a router that reported no filters,
					// which is now only one case of two.
					"treat the results as unfiltered", "does not yet apply to every provider",
				} {
					if strings.Contains(flat(skill), unwanted) {
						t.Errorf("skill still says %q", unwanted)
					}
				}

				// The default tier must not be credited with what only
				// balanced does: the unqualified claim this replaced.
				if strings.Contains(skill, "one call fans out across several search providers and returns provider-attributed results") {
					t.Error("skill still claims the default tier returns several attributed providers")
				}
			})
		}
	}
}

// TestSkillSaysASearchThatDidNotRunIsReported: dropin-miner#135. Cursor 3.21 sandboxed
// the first search and refused the second, and the agent then answered the
// user's question with no search having reached the router. The skill's error
// discipline said to decide from ok, retryable and action; it did not say
// what to do with the question itself. Asserted inside "What comes back",
// where the envelope is described, for every host on every OS.
func TestSkillSaysASearchThatDidNotRunIsReported(t *testing.T) {
	entry := binEntry{command: "/home/u/.jevlin/bin/jevlin", cfg: "/home/u/.jevlin/jevlin.toml"}
	for _, id := range goldenHostIDs {
		for _, goos := range hostShellOSes {
			t.Run(id+"/"+goos, func(t *testing.T) {
				section, ok := skillSection(renderedSkillFor(id, entry, goos), "## What comes back", "## Reading the answer")
				if !ok {
					t.Fatal("no ## What comes back section found")
				}
				for _, want := range []string{
					"When `ok` is false, or the command could not run at all",
					"tell the user the search did not run and stop",
					"never answer the question as if the search had run",
				} {
					if !containsFlat(section, want) {
						t.Errorf("What comes back does not say %q:\n%s", want, section)
					}
				}
			})
		}
	}
}

// The skill teaches reading a page in full: when, how, and what each
// answer that only a fetch gets means, with a runnable fetch for each shell
// the host runs; and it names source_id once, in Reading the answer. Every
// placeholder the template carries is filled.
func TestSkillTeachesReadingAPageInFull(t *testing.T) {
	entry := binEntry{command: "/home/u/.jevlin/bin/jevlin", cfg: "/home/u/.jevlin/jevlin.toml"}
	for _, id := range goldenHostIDs {
		for _, goos := range hostShellOSes {
			t.Run(id+"/"+goos, func(t *testing.T) {
				skill := renderedSkillFor(id, entry, goos)
				if strings.Contains(skill, "{{") || strings.Contains(skill, "}}") {
					t.Errorf("a placeholder is left in the rendered skill:\n%s", skill)
				}
				fetch, ok := skillSection(skill, "## Reading a page in full", "## Mining is not search")
				if !ok {
					t.Fatal("no ## Reading a page in full section found")
				}
				for _, want := range []string{
					"lacks the specific fact", "one to three pages", "mainly for a lookup",
					"not for a question whose answer is a list",
					"`next_offset`", "`offset` reads on", "`query`", "starts a long page's text",
					"`not_recent` was not in your recent Jevlin results",
					"fetch only pages a search returned", "for any other URL use your host's own fetch tool",
					"`fetch_unavailable` means the account is not claimed",
					"`fetch_not_supported`", "untrusted web content",
				} {
					if !strings.Contains(flat(fetch), want) {
						t.Errorf("the fetch section is missing %q\n%s", want, fetch)
					}
				}
				shells, _ := toolShellsForSkill(mustTarget(t, id), goos)
				for _, sh := range shells.kinds {
					_, script, err := fetchBlockForShell(sh, entry, exampleFetchRequest)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(fetch, script) {
						t.Errorf("the fetch section has no %s block:\n%s", sh, fetch)
					}
				}
				// No claim the host will run it without asking: hosts
				// installed before fetch was allowed still ask.
				for _, unwanted := range []string{"without asking", "without a prompt", "no prompt", "pre-approved", "auto-allow"} {
					if strings.Contains(flat(fetch), unwanted) {
						t.Errorf("the fetch section promises %q", unwanted)
					}
				}
				reading, _ := skillSection(skill, "## Reading the answer", "## Reading a page in full")
				if !strings.Contains(flat(reading), "the same page found twice has the same `source_id`") {
					t.Errorf("Reading the answer does not say what source_id is:\n%s", reading)
				}
				if n := strings.Count(flat(skill), "found twice"); n != 1 {
					t.Errorf("source_id's stability is said %d times, want once", n)
				}
			})
		}
	}
}

func mustTarget(t *testing.T, id string) installTarget {
	t.Helper()
	tg, ok := targetByID(installTargets, id)
	if !ok {
		t.Fatalf("no target %q", id)
	}
	return tg
}
