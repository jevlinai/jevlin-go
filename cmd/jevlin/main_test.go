package main

import (
	"strings"
	"testing"
)

// section extracts the text between two markers in usageText, in order,
// so a reshuffle of the help text cannot pass a check by matching a
// stray occurrence of the marker string somewhere else in the constant.
func section(t *testing.T, from, to string) string {
	t.Helper()
	start := strings.Index(usageText, from)
	if start == -1 {
		t.Fatalf("usageText: start marker %q not found", from)
	}
	end := strings.Index(usageText[start:], to)
	if end == -1 {
		t.Fatalf("usageText: end marker %q not found after %q", to, from)
	}
	return usageText[start : start+end]
}

func TestUsageTextSearchSection(t *testing.T) {
	s := section(t, "\n  search ", "\n  agents ")

	for _, want := range []string{"--stdin", "-tier", "-format", "valid search response", "invalid server response"} {
		if !strings.Contains(s, want) {
			t.Errorf("search section: missing %q\n%s", want, s)
		}
	}

	if !strings.Contains(s, "-timeout") {
		t.Errorf("search section: missing -timeout\n%s", s)
	}
	if !strings.Contains(s, defaultSearchTimeout.String()) {
		t.Errorf("search section: does not advertise the real default timeout %q\n%s",
			defaultSearchTimeout.String(), s)
	}
}

func TestUsageTextAgentsSection(t *testing.T) {
	s := section(t, "\n  agents ", "\n  hook ")

	for _, host := range []string{"claude", "codex", "cursor", "opencode", "pi", "hermes"} {
		if !strings.Contains(s, host) {
			t.Errorf("agents section: missing host %q\n%s", host, s)
		}
	}
}

func TestUsageTextConnectSection(t *testing.T) {
	s := section(t, "\n  connect ", "\n  login ")

	if !strings.Contains(s, "-json") {
		t.Errorf("connect section: missing -json\n%s", s)
	}
	if !strings.Contains(s, "instead of narration") {
		t.Errorf("connect section: does not say -json replaces the narration\n%s", s)
	}
}

func TestUsageTextStatusSection(t *testing.T) {
	s := section(t, "\n  status ", "\nis it working")

	if !strings.Contains(s, "-json") {
		t.Errorf("status section: missing -json\n%s", s)
	}
}

func TestUsageTextDoctorSection(t *testing.T) {
	s := section(t, "\n  doctor ", "\n  limits ")

	if !strings.Contains(s, "-json") {
		t.Errorf("doctor section: missing -json\n%s", s)
	}

	// Matched against the section with its whitespace collapsed: the help is
	// hand-wrapped, so a phrase can straddle a line break, and a reflow is
	// not the drift this guards against. The wording is the assertion.
	flat := strings.Join(strings.Fields(s), " ")

	// Every check doctor runs is named, and the exit rule is stated as it
	// is: a NO is a diagnosis, not a failure of the command.
	for _, want := range []struct{ phrase, why string }{
		{"stored search key", "the key check"},
		{"registration", "the registration check"},
		{"trace key", "the trace key check"},
		{"latest release", "the release check"},
		{"binary on PATH", "the binary check"},
		{"state access", "the Windows access-list check"},
		{"a NO is a successful diagnosis", "the exit rule"},
	} {
		if !strings.Contains(flat, want.phrase) {
			t.Errorf("doctor section: missing %q — %s\n%s", want.phrase, want.why, s)
		}
	}

	// The checks an earlier version ran against the mining plane are not
	// promised: a help text that still named them would send a participant
	// looking for a line doctor no longer prints.
	for _, gone := range []string{"enrolled", "earning", "intake", "probe", "wallet access"} {
		if strings.Contains(flat, gone) {
			t.Errorf("doctor section: still names %q, a check this client no longer runs\n%s", gone, s)
		}
	}
}
