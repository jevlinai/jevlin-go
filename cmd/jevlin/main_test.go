package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// helpOf is one command's help, failing the test when it has none.
func helpOf(t *testing.T, name string) string {
	t.Helper()
	h, ok := commandHelp(name)
	if !ok {
		t.Fatalf("no help for %q", name)
	}
	return h
}

func TestSearchHelp(t *testing.T) {
	s := helpOf(t, "search")

	for _, want := range []string{"--stdin", "-tier", "-format", "valid search response", "invalid server response"} {
		if !strings.Contains(s, want) {
			t.Errorf("search help: missing %q\n%s", want, s)
		}
	}

	if !strings.Contains(s, "-timeout") {
		t.Errorf("search help: missing -timeout\n%s", s)
	}
	if !strings.Contains(s, defaultSearchTimeout.String()) {
		t.Errorf("search help: does not advertise the real default timeout %q\n%s",
			defaultSearchTimeout.String(), s)
	}
}

func TestAgentsHelpNamesEveryHost(t *testing.T) {
	s := helpOf(t, "agents")

	for _, host := range []string{"claude", "codex", "cursor", "opencode", "pi", "hermes"} {
		if !strings.Contains(s, host) {
			t.Errorf("agents help: missing host %q\n%s", host, s)
		}
	}
}

func TestConnectHelp(t *testing.T) {
	s := helpOf(t, "connect")

	if !strings.Contains(s, "-json") {
		t.Errorf("connect help: missing -json\n%s", s)
	}
	if !strings.Contains(s, "instead of narration") {
		t.Errorf("connect help: does not say -json replaces the narration\n%s", s)
	}
}

func TestStatusHelp(t *testing.T) {
	s := helpOf(t, "status")

	if !strings.Contains(s, "-json") {
		t.Errorf("status help: missing -json\n%s", s)
	}
}

func TestDoctorHelp(t *testing.T) {
	s := helpOf(t, "doctor")

	if !strings.Contains(s, "-json") {
		t.Errorf("doctor help: missing -json\n%s", s)
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
			t.Errorf("doctor help: missing %q — %s\n%s", want.phrase, want.why, s)
		}
	}

	// Checks this client does not run are not promised: a help text that
	// named one would send a participant looking for a line doctor does not
	// print.
	for _, gone := range []string{"enrolled", "earning", "intake", "probe", "payout access"} {
		if strings.Contains(flat, gone) {
			t.Errorf("doctor help: still names %q, a check this client no longer runs\n%s", gone, s)
		}
	}
}

// fetch -h and agents -h answer with their own usage on stdout and exit 0,
// and the agents status usage line offers only the flags status acts on.
func TestSubcommandHelpListsTheFlagsThatApply(t *testing.T) {
	noEnv := func(string) string { return "" }

	var out, errOut bytes.Buffer
	if code := fetchMain(time.Now, []string{"-h"}, strings.NewReader(""), &out, &errOut, noEnv); code != exitOK {
		t.Fatalf("fetch -h: exit %d, stderr %q", code, errOut.String())
	}
	for _, want := range []string{"--stdin", "-config", "-timeout", "(default " + defaultSearchTimeout.String() + ")"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("fetch -h: missing %q\n%s", want, out.String())
		}
	}

	out.Reset()
	errOut.Reset()
	if code := agentsMain(realAgentOps(), []string{"-h"}, strings.NewReader(""), &out, &errOut, noEnv); code != exitOK {
		t.Fatalf("agents -h: exit %d, stderr %q", code, errOut.String())
	}
	if out.String() != agentsUsage {
		t.Errorf("agents -h printed %q, want agentsUsage", out.String())
	}

	// agents status reads and prints, so its usage line offers neither
	// -dry-run nor -yes: they would promise a plan and a question that never
	// come. Its line is the one that begins with "jevlin agents status".
	var statusLine string
	for _, line := range strings.Split(agentsUsage, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "jevlin agents status ") {
			statusLine = line // the usage line, which comes before the examples
			break
		}
	}
	if !strings.Contains(statusLine, "-client") || !strings.Contains(statusLine, "-config") {
		t.Fatalf("agents help: no status usage line naming -client and -config\n%s", agentsUsage)
	}
	for _, gone := range []string{"-dry-run", "-yes"} {
		if strings.Contains(statusLine, gone) {
			t.Errorf("agents status usage offers %s, which status ignores: %s", gone, statusLine)
		}
	}
}
