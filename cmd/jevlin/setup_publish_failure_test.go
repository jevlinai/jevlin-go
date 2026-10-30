package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// When setup has set an older config aside and then cannot publish the new
// one, the installation must not be left with no jevlin.toml, which every
// command would read as "not set up yet" and run on compiled defaults: the
// older file goes back, and the message says where it is.
func TestSetupPutsTheOlderConfigBackWhenPublishingFails(t *testing.T) {
	s := newSetupSandbox(t)
	legacy := []byte("[[provider]]\nname = \"search-router\"\nupstream = \"https://api.jevlin.ai\"\n")
	if err := os.MkdirAll(s.home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.cfgPath(), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	prev := publishConfig
	publishConfig = func(string, []byte) error { return errors.New("disk full") }
	t.Cleanup(func() { publishConfig = prev })

	code, _, errOut := s.run(nil, true, "-yes", "-no-profile", "-no-agents")
	if code == exitOK {
		t.Fatal("setup exited 0 without a config")
	}
	if got, err := os.ReadFile(s.cfgPath()); err != nil || string(got) != string(legacy) { // #nosec G304 -- this test's sandbox
		t.Errorf("the older config was not put back byte for byte: %q, %v", got, err)
	}
	if lexists(s.cfgPath() + ".old") {
		t.Error("the set-aside copy was left beside the restored file")
	}
	if !strings.Contains(errOut, "disk full") || !strings.Contains(errOut, "put back at "+s.cfgPath()) {
		t.Errorf("the message does not say the publish failed and where the older file is:\n%s", errOut)
	}
	if s.connectCalls != 0 {
		t.Error("connect ran without a config")
	}
	// Nothing else of the config step's: the directories setup made before
	// it and its lock are the installation's own, and stay.
	if entries, _ := os.ReadDir(s.home); len(entries) != 0 {
		for _, e := range entries {
			switch e.Name() {
			case setupConfigFile, "state", "sessions", setupLockFile:
			default:
				t.Errorf("left behind: %s", filepath.Join(s.home, e.Name()))
			}
		}
	}
}
