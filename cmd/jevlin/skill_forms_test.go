package main

// The query forms in the skill, behind skillTeachesQueryForms (the switch
// for Jevlin-core #55): off, the skill says nothing about them or about
// what #55 adds to the merged view; on, which is how it ships, it teaches
// both, with examples a --stdin request accepts. These tests set a package
// variable, so each restores it with t.Cleanup and none is parallel: a
// parallel test rendering a skill would see the switch.

import (
	"regexp"
	"strings"
	"testing"
)

func withQueryFormsTaught(t *testing.T, on bool) {
	t.Helper()
	was := skillTeachesQueryForms
	skillTeachesQueryForms = on
	t.Cleanup(func() { skillTeachesQueryForms = was })
}

// The switch ships on. The tests below set it themselves, so none would
// notice the default turned off; this one reads it as compiled, and reads
// the skill a participant gets. Turning it off, if the live router stops
// taking the forms, is a deliberate change, and this test changes with it.
func TestTheQueryFormsAreTaughtAsShipped(t *testing.T) {
	if !skillTeachesQueryForms {
		t.Fatal("skillTeachesQueryForms is off: the skill no longer teaches agents to send objective and " +
			"search_queries, though Jevlin-core #55 is live. Turn it off only if the router stops taking them, " +
			"and change this test in the same commit.")
	}
	entry := binEntry{command: "/home/u/.jevlin/bin/jevlin", cfg: "/home/u/.jevlin/jevlin.toml"}
	for _, id := range goldenHostIDs {
		skill := renderedSkillFor(id, entry, hostShellOSes[0])
		for _, want := range []string{"`objective`", "`search_queries`", "`result.forms_dropped`"} {
			if !strings.Contains(skill, want) {
				t.Errorf("%s: the skill as shipped does not say %s", id, want)
			}
		}
	}
}

// Off is the state before #55 was live: a skill that told every agent to
// send the forms to a router that refuses them would make every search
// pay a refused request and a retry. It is kept as the proof that the
// switch still switches.
func TestTheSkillTeachesNoQueryFormsUntilTheRouterTakesThem(t *testing.T) {
	withQueryFormsTaught(t, false)
	entry := binEntry{command: "/home/u/.jevlin/bin/jevlin", cfg: "/home/u/.jevlin/jevlin.toml"}
	for _, id := range goldenHostIDs {
		for _, goos := range hostShellOSes {
			skill := renderedSkillFor(id, entry, goos)
			for _, unwanted := range []string{"objective", "search_queries", "votes", "coverage", "merged_total", "forms_dropped", "{{"} {
				if strings.Contains(skill, unwanted) {
					t.Errorf("%s/%s: the skill says %q with the query forms gated off", id, goos, unwanted)
				}
			}
		}
	}
}

// requestExample is a request example as the skill writes one: inline code
// holding a version-1 request.
var requestExample = regexp.MustCompile("`(\\{\"version\":1[^`]*\\})`")

func TestTheSkillTeachesTheQueryFormsOnceReleased(t *testing.T) {
	withQueryFormsTaught(t, true)
	entry := binEntry{command: "/home/u/.jevlin/bin/jevlin", cfg: "/home/u/.jevlin/jevlin.toml"}
	for _, id := range goldenHostIDs {
		for _, goos := range hostShellOSes {
			skill := renderedSkillFor(id, entry, goos)
			if strings.Contains(skill, "{{") {
				t.Fatalf("%s/%s: a template marker is left in the skill", id, goos)
			}
			options, ok := skillSection(skill, "## Request options", "## What comes back")
			if !ok {
				t.Fatal("no ## Request options section found")
			}
			// When: objective on every search; search_queries when the
			// first came back thin or a name is uncertain, and in which
			// three wordings. And that a dropped form is not a failure.
			prose := strings.Join(strings.Fields(options), " ") // wrapping is not what is asserted
			for _, want := range []string{
				"`objective`", "on every search", "`search_queries`", "costs about as much as another search", "came back thin", "not sure of",
				"as the user wrote it", "the vendor's name", "plain-words", "`result.forms_dropped`", "not a failure",
			} {
				if !strings.Contains(prose, want) {
					t.Errorf("%s/%s: the Request options section does not say %q", id, goos, want)
				}
			}
			// Every example is a request this client accepts, and the two
			// new ones carry the field they teach.
			sawObjective, sawForms := false, false
			for _, m := range requestExample.FindAllStringSubmatch(options, -1) {
				req, err := decodeMachineSearchRequest(strings.NewReader(m[1]))
				if err != nil {
					t.Errorf("%s/%s: the skill's example %s is refused: %v", id, goos, m[1], err)
					continue
				}
				sawObjective = sawObjective || req.objective != nil
				sawForms = sawForms || len(req.searchQueries) > 0
			}
			if !sawObjective || !sawForms {
				t.Errorf("%s/%s: an example with objective %v, with search_queries %v; want one of each", id, goos, sawObjective, sawForms)
			}
			reading, ok := skillSection(skill, "## Reading the answer", "")
			if !ok {
				t.Fatal("no ## Reading the answer section found")
			}
			reading = strings.Join(strings.Fields(reading), " ")
			for _, want := range []string{"`votes`", "`result.coverage`", "fan-out's own evidence", "every provider's text", "`result.merged_total`"} {
				if !strings.Contains(reading, want) {
					t.Errorf("%s/%s: the Reading the answer section does not say %q", id, goos, want)
				}
			}
		}
	}
}
