package main

// The query forms in the skill, behind skillTeachesQueryForms (the release
// gate for Jevlin-core #55): off, the skill says nothing about them or about
// what #55 adds to the merged view; on, it teaches both, with examples a
// --stdin request accepts. These tests set a package variable, so each
// restores it with t.Cleanup and none is parallel: a parallel test
// rendering a skill would see the switch.

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

// The gate ships off. The tests below set it themselves, so neither would
// notice the default turned on; this one reads it as compiled. Turning the
// gate on, once Jevlin-core #55 is live on the router, is the release's
// deliberate change, and this test changes with it.
func TestTheQueryFormsGateShipsOff(t *testing.T) {
	if skillTeachesQueryForms {
		t.Fatal("skillTeachesQueryForms is on: the skill would teach every agent to send objective and " +
			"search_queries. Turn it on only once the live router takes them, and change this test in the same commit.")
	}
}

// Off is the shipped state until #55 is live: a skill that told every
// agent to send the forms to a router that refuses them would make every
// search pay a refused request and a retry.
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
			reading, ok := skillSection(skill, "## Reading the answer", "## Mining is not search")
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
