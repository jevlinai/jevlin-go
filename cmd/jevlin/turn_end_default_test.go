package main

// The turn end is on for an installation setup creates and off for every
// other one. setup writes `turn_end = true` only into a config it creates
// where there was none; the compiled default stays off, so a config without
// the line, which is every config an earlier version wrote, reads as off and
// sends no turn end. That is what keeps an existing installation as it was
// through an upgrade, with no migration.

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

// turnEndLineRe is the line setup writes, wherever it sits in [search].
var turnEndLineRe = regexp.MustCompile(`(?m)^turn_end\s*=\s*true$`)

// A fresh setup writes the line and the config it wrote loads with the turn
// end on, with no terminal and with -yes alike: setup asks nothing about it.
func TestAFreshSetupTurnsTheTurnEndOn(t *testing.T) {
	for name, args := range map[string][]string{
		"no terminal": nil,
		"-yes":        {"-yes"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSetupSandbox(t)
			s.platform.claim("credits")
			if lexists(s.cfgPath()) {
				t.Fatal("the sandbox already holds a config")
			}
			code, out, errOut := s.run(nil, false, args...)
			if code != exitOK {
				t.Fatalf("setup exited %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
			}
			body := s.readFile(s.cfgPath())
			if !turnEndLineRe.Match(body) {
				t.Errorf("the fresh config has no turn_end = true line:\n%s", body)
			}
			if !loadSetupConfig(t, s.cfgPath()).Search.TurnEnd {
				t.Errorf("the fresh config loads with the turn end off:\n%s", body)
			}
		})
	}
}

// A rerun on a config without the line leaves it without the line, byte for
// byte, and it still loads with the turn end off. The same holds for a second
// rerun, so nothing about the first one changed the file for the next.
func TestASetupRerunLeavesAConfigWithoutTheLineWithoutIt(t *testing.T) {
	s := newSetupSandbox(t)
	s.platform.claim("credits")
	if err := os.MkdirAll(s.home, 0o700); err != nil {
		t.Fatal(err)
	}
	// What setup wrote before this change: the fresh config, no turn_end.
	before, err := renderFreshConfig(resolveSetupValues(s.home, s.getenv), false)
	if err != nil {
		t.Fatal(err)
	}
	if turnEndLineRe.Match(before) || bytes.Contains(before, []byte("turn_end")) {
		t.Fatalf("the fixture already names turn_end:\n%s", before)
	}
	if err := os.WriteFile(s.cfgPath(), before, 0o600); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		code, out, errOut := s.run(nil, false, "-yes")
		if code != exitOK {
			t.Fatalf("run %d: setup exited %d\nstdout:\n%s\nstderr:\n%s", run, code, out, errOut)
		}
		if got := s.readFile(s.cfgPath()); !bytes.Equal(got, before) {
			t.Fatalf("run %d changed the config:\n--- want ---\n%s\n--- got ---\n%s", run, before, got)
		}
		if loadSetupConfig(t, s.cfgPath()).Search.TurnEnd {
			t.Fatalf("run %d: a config without the line loads with the turn end on", run)
		}
	}
}

// An older config set aside is replaced by a file without the line, whatever
// the older one said: the replacement is not a config setup creates where
// there was none.
func TestAReplacedOlderConfigDoesNotTurnTheTurnEndOn(t *testing.T) {
	for name, legacy := range map[string]string{
		"provider only":    "[[provider]]\nname = \"search-router\"\nupstream = \"https://router.invalid.test\"\n",
		"old search table": "[search]\nrouter_url = \"https://router.invalid.test\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			s := newSetupSandbox(t)
			s.platform.claim("credits")
			if err := os.MkdirAll(s.home, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.cfgPath(), []byte(legacy), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, errOut := s.run(nil, false, "-yes")
			if code != exitOK {
				t.Fatalf("setup exited %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
			}
			if !lexists(s.cfgPath() + ".old") {
				t.Fatalf("the older config was not set aside, so this is not the replacement:\n%s", out)
			}
			body := s.readFile(s.cfgPath())
			if strings.Contains(string(body), "turn_end") || loadSetupConfig(t, s.cfgPath()).Search.TurnEnd {
				t.Errorf("the replacement turned the turn end on:\n%s", body)
			}
		})
	}
}

// A config without the line sends no turn end: the real Stop entry, over a
// config on disk that names turn_end nowhere, queues nothing and asks for no
// sender for a turn in which a search was served. The control is the same
// config with the line setup writes, which does.
func TestAConfigWithoutTheLineSendsNoTurnEnd(t *testing.T) {
	turn := traceHash("sess|p1")

	on := newTurnEndInstallation(t, true)
	on.served(t, turn)
	onAsks := watchSpawns(t)
	stopHook(t, on, claudeStop(nil))
	if len(*onAsks) != 1 {
		t.Fatalf("control: with turn_end = true the served turn asked for %d sender(s), want 1", len(*onAsks))
	}

	off := newTurnEndInstallation(t, true)
	b, err := os.ReadFile(off.cfg) // #nosec G304 -- this test's own config under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	without := strings.Replace(string(b), "turn_end = true\n", "", 1)
	if without == string(b) || strings.Contains(without, "turn_end") {
		t.Fatalf("the fixture's line was not removed:\n%s", without)
	}
	if err := os.WriteFile(off.cfg, []byte(without), 0o600); err != nil { // #nosec G703 -- the fixture's own config under t.TempDir
		t.Fatal(err)
	}
	// The mark a served search leaves, planted directly: under this config
	// the search itself leaves none, and what is held here is that the Stop
	// hook, finding one, still sends nothing.
	markTurnSearched(realHookOps(), config.Search{TurnEnd: true, SessionsDir: off.sessions}, &traceEnvelope{TurnID: turn})
	offAsks := watchSpawns(t)
	stopHook(t, off, claudeStop(nil))
	if len(*offAsks) != 0 {
		t.Errorf("a config without the line asked for a sender: %+v", *offAsks)
	}
	if files := off.queuedTurnEnds(t); len(files) != 0 {
		t.Errorf("a config without the line queued %q", files)
	}
}
