package main

import (
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// The skill is two files, and an agent may read SKILL.md alone: the call,
// the request and how to read the answer must be there, not only in
// reference.md. And reference.md, which the guidance guards read together
// with SKILL.md, must hold none of the superseded rules on its own either.
func TestSkillMdAloneCarriesTheCallAndTheReferenceNoSupersededRule(t *testing.T) {
	entry := guidanceEntry()
	// The guidance guards' own patterns already read the pair together.
	superseded := []string{`(?i)never retry\b`, `(?i)do not retry the search`, `(?i)do not silently fall back`,
		`(?i)401[^.]{0,40}has not registered`}
	for _, s := range targetsByKind(targetHost) {
		if s.ID() == "opencode" {
			continue // opencode's instruction text is its plugin's, not a skill pair
		}
		skill, reference := renderedSkillPairFor(t, s.ID(), entry, runtime.GOOS, preferOn)
		for _, want := range []string{"--stdin", `"version"`, "query", "ok", "retryable", "action"} {
			if !strings.Contains(skill, want) {
				t.Errorf("%s: SKILL.md alone lacks %q", s.ID(), want)
			}
		}
		for _, f := range superseded {
			if regexp.MustCompile(f).MatchString(reference) {
				t.Errorf("%s: reference.md says %s", s.ID(), f)
			}
		}
	}
}
