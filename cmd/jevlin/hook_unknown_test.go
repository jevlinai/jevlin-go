package main

import (
	"bytes"
	"strings"
	"testing"
)

// A hook entry is written by `agents install` and run by the host, never
// typed by a person, so a subcommand this binary does not know is a hook
// file and a binary of different versions. Invariant 1: the hook never fails
// the tool call it runs under, so the entry is named on stderr, nothing is
// written or spawned, and the exit is 0 — not the usage exit a person's
// mistyped command earns.
func TestAnUnknownHookSubcommandIsHarmless(t *testing.T) {
	cfg := writeHookConfig(t)
	for _, args := range [][]string{
		{"flush"},
		{"flush", "extra"},
		{"nonesuch"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			fs, ops := newFakeHookOps(nil)
			var checks int
			ops = countingChecks(ops, &checks)
			var stdout, stderr bytes.Buffer
			full := append([]string{"-config", cfg}, args...)
			payload := map[string]any{"session_id": "s", "hook_event_name": "Stop"}
			if code := hookMain(ops, full, bytes.NewReader(mustJSON(t, payload)), &stdout, &stderr); code != exitOK {
				t.Fatalf("exit %d; an unknown hook subcommand must not fail the host's tool call", code)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout %q; an unknown subcommand answers the host nothing", stdout.String())
			}
			if !strings.Contains(stderr.String(), "unknown subcommand") || !strings.Contains(stderr.String(), args[0]) {
				t.Errorf("stderr %q; the entry is named so a person reading the host's log can see the skew", stderr.String())
			}
			if len(fs.files) != 0 || checks != 0 {
				t.Errorf("files %v, release checks %d; an unknown subcommand writes and spawns nothing", keys(fs.files), checks)
			}
		})
	}
}
