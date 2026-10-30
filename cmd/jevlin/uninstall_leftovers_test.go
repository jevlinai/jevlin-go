package main

// What a confirmed -purge-state leaves in the installation directory, and
// what it says about it. An installation that came from an earlier version
// through setup carries files only that version used, and one of them holds
// a key; this client keeps no list of their names and removes none of them,
// so a purge that said nothing left a participant who had just destroyed
// their identity with files they could not place. A purge now names, with
// its full path, every entry it did not remove. The configurations setup set
// aside as jevlin.toml.old, .old.1, ... are this installation's, and go.

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const leftInHomeHeading = "not this version's to remove: files an earlier version or another program put"

// leftovers plants, in an installed sandbox, the two kinds of entry a purge
// tells apart: the set-aside configurations setup wrote, which are the
// installation's own, and entries nothing of this version wrote — a
// directory with a file in it, a file, and a file whose name only resembles
// a set-aside configuration, so the match is held to asideName's exact
// spellings.
func leftovers(t *testing.T, s *setupSandbox) (ours, strays []string) {
	t.Helper()
	h := func(rel string) string { return filepath.Join(s.home, rel) }
	ours = []string{h(setupConfigFile + ".old"), h(setupConfigFile + ".old.1")}
	strays = []string{h("earlier-dir"), h("earlier.key"), h(setupConfigFile + ".old.bak")}
	writeFileT(t, ours[0], "schema-less = true\n")
	writeFileT(t, ours[1], "schema-less = 2\n")
	writeFileT(t, filepath.Join(strays[0], "inner.json"), `{"kept":true}`)
	writeFileT(t, strays[1], "not a key this version issued")
	writeFileT(t, strays[2], "a name that only resembles a set-aside configuration")
	return ours, strays
}

// leftSection is the part of out that names what a purge left in home, from
// the start of its heading line to the next blank line: the assertions are
// about that list, not about the plan or the removal lines, which print the
// same paths.
func leftSection(t *testing.T, out string) string {
	t.Helper()
	idx := strings.Index(out, leftInHomeHeading)
	if idx < 0 {
		t.Fatalf("the output does not say what is left in the installation directory:\n%s", out)
	}
	idx = strings.LastIndex(out[:idx], "\n") + 1
	section := out[idx:]
	if end := strings.Index(section, "\n\n"); end >= 0 {
		section = section[:end]
	}
	return section
}

// listed reports whether the section names p as one of its own lines: a
// substring would take jevlin.toml.old for jevlin.toml.old.bak.
func listed(section, p string) bool {
	for _, line := range strings.Split(section, "\n") {
		if strings.TrimSpace(line) == p {
			return true
		}
	}
	return false
}

func TestPurgeRemovesSetAsideConfigsAndNamesWhatItLeaves(t *testing.T) {
	s := installed(t)
	ours, strays := leftovers(t, s)
	before := map[string]map[string]fileSig{}
	for _, p := range strays {
		before[p] = snapshotTree(t, p)
	}

	code, out, errOut := s.uninstall(t, tty(s.home), true, "-purge-state")
	if code != exitOK {
		t.Fatalf("purge exited %d\n%s\n%s", code, out, errOut)
	}
	for _, p := range ours {
		if lexists(p) {
			t.Errorf("a configuration setup set aside is this installation's, and the purge left %s", p)
		}
	}
	for _, p := range strays {
		if !lexists(p) {
			t.Fatalf("the purge removed %s, which nothing of this version wrote", p)
		}
		if !reflect.DeepEqual(before[p], snapshotTree(t, p)) {
			t.Errorf("the purge changed %s, which nothing of this version wrote", p)
		}
	}

	section := leftSection(t, out)
	if !strings.HasPrefix(section, "Left in "+s.home+", ") {
		t.Errorf("the real run's heading says it left them, naming the directory:\n%s", section)
	}
	for _, p := range strays {
		if !listed(section, p) {
			t.Errorf("the purge does not name %s, which it left, with its full path:\n%s", p, out)
		}
	}
	for _, p := range ours {
		if listed(section, p) {
			t.Errorf("%s was removed and must not be called somebody else's:\n%s", p, section)
		}
	}
	for _, line := range strings.Split(section, "\n")[1:] {
		if strings.HasSuffix(line, ".lock") {
			t.Errorf("a lock file is named once, by the locks section, not here as well: %q", line)
		}
	}
	// The locks section still names the gate, after this one.
	if idx := strings.Index(out, leftoverHeading); idx < 0 || !strings.Contains(out[idx:], lifecycleGatePath(s.home)) {
		t.Errorf("the locks section must still name the gate:\n%s", out)
	}
}

func TestPurgeDryRunNamesWhatItWouldLeaveAndChangesNothing(t *testing.T) {
	s := installed(t)
	ours, strays := leftovers(t, s)
	before := snapshotHeld(t, s.root)

	code, out, errOut := s.uninstall(t, nil, false, "-purge-state", "-dry-run")
	if code != exitOK {
		t.Fatalf("dry run exited %d\n%s\n%s", code, out, errOut)
	}
	if !reflect.DeepEqual(withoutLocks(before), withoutLocks(snapshotHeld(t, s.root))) {
		t.Error("a dry run changes nothing")
	}
	section := leftSection(t, out)
	if !strings.HasPrefix(section, "Would leave in "+s.home+", ") {
		t.Errorf("the dry run says what it would leave, naming the directory:\n%s", section)
	}
	for _, p := range strays {
		if !listed(section, p) {
			t.Errorf("the dry run does not name %s as left, with its full path:\n%s", p, out)
		}
	}
	for _, p := range ours {
		if listed(section, p) {
			t.Errorf("%s is in the purge set and must not be listed as left:\n%s", p, section)
		}
		if !strings.Contains(out, "  remove "+p+"\n") {
			t.Errorf("the dry run must plan the removal of %s:\n%s", p, out)
		}
	}
	for _, line := range strings.Split(section, "\n")[1:] {
		if strings.HasSuffix(line, ".lock") {
			t.Errorf("a lock file is named once, by the locks section, not here as well: %q", line)
		}
	}
}

// A purge that leaves the directory empty removes it and has nothing to
// name; the heading is not printed over an empty list.
func TestPurgeThatLeavesNothingSaysNothingAboutIt(t *testing.T) {
	s := installed(t)
	code, out, errOut := s.uninstall(t, tty(s.home), true, "-purge-state")
	if code != exitOK {
		t.Fatalf("purge exited %d\n%s\n%s", code, out, errOut)
	}
	if lexists(s.home) {
		t.Fatalf("this case is meant to leave the directory empty and removed, and %s is still there", s.home)
	}
	if strings.Contains(out, leftInHomeHeading) {
		t.Errorf("nothing was left, so nothing is named as left:\n%s", out)
	}
}

// The installation's own binary directory is not somebody else's: a purge
// leaves it to -binary and says so, rather than listing it among files an
// earlier version or another program put there.
func TestPurgeLeavesTheBinaryDirectoryToBinaryAndSaysSo(t *testing.T) {
	s := installedWithOwnBinary(t)
	code, out, errOut := s.uninstall(t, tty(s.home), true, "-purge-state")
	if code != exitOK {
		t.Fatalf("purge exited %d\n%s\n%s", code, out, errOut)
	}
	if strings.Contains(out, leftInHomeHeading) {
		t.Errorf("bin/ is this installation's and must not be listed as somebody else's:\n%s", out)
	}
	if !strings.Contains(out, filepath.Dir(s.exe)+" holds this installation's binary") {
		t.Errorf("the purge says the binary directory stays for -binary:\n%s", out)
	}
}

// An entry of the purge set that is still there after the run was named as
// a problem by applyPurge, and must not be called somebody else's as well.
// On the honest path every purged entry is gone before the summary, so the
// exclusion answers only when a removal failed, which the harness cannot make
// happen on every runner: the state is built here, at the unit, by handing
// sayLeftInHome a purged entry that still exists.
func TestAnEntryTheRunMeantToRemoveIsNotCalledSomebodyElses(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".jevlin")
	kept := filepath.Join(home, credentialsFile)
	stray := filepath.Join(home, "earlier.key")
	writeFileT(t, kept, `{"api_key":"sr-not-removed"}`)
	writeFileT(t, stray, "not this version's")
	var out bytes.Buffer
	r := &uninstallRun{d: uninstallDeps{stdout: &out, stderr: &out}, home: home, purge: true}
	r.sayLeftInHome([]string{kept}, false)
	section := leftSection(t, out.String())
	if !listed(section, stray) {
		t.Errorf("the stray entry is named:\n%s", out.String())
	}
	if listed(section, kept) {
		t.Errorf("%s was in the purge set, so a run that left it named a problem, not a file of somebody else's:\n%s", kept, out.String())
	}
}
