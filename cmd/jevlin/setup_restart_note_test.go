package main

import (
	"strings"
	"testing"
)

// closingBox is setup's closing message: between the last two rules.
func closingBox(t *testing.T, out string) string {
	t.Helper()
	rule := strings.Repeat("─", 78)
	end := strings.LastIndex(out, rule)
	start := strings.LastIndex(out[:max(end, 0)], rule)
	if start < 0 {
		t.Fatalf("no closing box:\n%s", out)
	}
	return out[start:end]
}

// restartAdvice reports whether text holds either sentence restartNote
// writes. (A plain search for "Restart" would match this test's own name in
// the sandbox's temporary paths.)
func restartAdvice(text string) bool {
	return strings.Contains(text, "already open, so") || strings.Contains(text, "without a restart.")
}

// The closing box says which agents need restarting, at its top, right
// under the headline: Claude Code was seen to pick up a new skill and hooks
// in a session already open, so it is told no restart is needed; every
// other host this run set up is told to restart; and a run that set up no
// host says nothing about restarting.
func TestSetupSaysWhichAgentsToRestartAtTheTopOfTheClosingBox(t *testing.T) {
	const claude = "Claude Code picks up the new skill and hooks without a restart.\n"
	for name, tc := range map[string]struct {
		args []string
		want []string // lines that must follow the headline, in order
	}{
		"Claude Code alone":      {[]string{"-with", "claude"}, []string{claude}},
		"Codex alone":            {[]string{"-with", "codex"}, []string{"Restart Codex if it is already open, so it loads what setup installed.\n"}},
		"Claude Code and others": {[]string{"-with", "claude", "-with", "cursor", "-with", "codex"}, []string{claude, "Restart Cursor and Codex if they are already open, so they load what setup installed.\n"}},
		"no agent set up":        {nil, nil},
		"a dry run":              {[]string{"-dry-run", "-with", "codex"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSetupSandbox(t)
			s.platform.claim("credits")
			code, out, errOut := s.run(nil, false, tc.args...)
			if code != exitOK {
				t.Fatalf("setup exited %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
			}
			box := closingBox(t, out)
			if strings.Contains(box, "Restart any coding agent") {
				t.Errorf("the closing box still tells every agent to restart:\n%s", box)
			}
			first := box[:strings.Index(box, "\n1. ")]
			if len(tc.want) == 0 {
				if restartAdvice(first) {
					t.Errorf("a run that set up no agent talks about restarting:\n%s", first)
				}
				return
			}
			at := 0
			for _, w := range tc.want {
				i := strings.Index(first[at:], w)
				if i < 0 {
					t.Fatalf("the top of the closing box does not say %q in order:\n%s", w, first)
				}
				at += i + len(w)
			}
			if name == "Claude Code alone" && strings.Contains(first, "already open, so") {
				t.Errorf("Claude Code alone is told to restart:\n%s", first)
			}
		})
	}
	// A second run writes nothing, so nothing new needs loading.
	s := newSetupSandbox(t)
	s.platform.claim("credits")
	if code, out, errOut := s.run(nil, false, "-with", "codex"); code != exitOK {
		t.Fatalf("first run exited %d\n%s\n%s", code, out, errOut)
	}
	code, out, errOut := s.run(nil, false, "-with", "codex")
	if code != exitOK {
		t.Fatalf("second run exited %d\n%s\n%s", code, out, errOut)
	}
	if box := closingBox(t, out); restartAdvice(box) {
		t.Errorf("a run that wrote nothing tells an agent to restart:\n%s", box)
	}
}
