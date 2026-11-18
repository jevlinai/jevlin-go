package main

// A lock file a finished operation created and did not remove is a file a
// person auditing a machine finds and cannot place. The gate always named
// itself as safe to delete; these are the rest of them.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const leftoverHeading = "Left, and safe to delete"

// updateLockFor is what an upgrade leaves beside the installation's binary.
func updateLockFor(s *setupSandbox) string {
	return filepath.Join(s.home, "bin", binaryNameFor()) + updateLockSuffix
}

// allLocks puts every lock file a Jevlin command leaves on disk and returns
// them in the order the summary names them. The gate, setup.lock and
// connect.lock are there from the real setup this sandbox ran; the update
// lock is written because no upgrade runs in a test sandbox.
func allLocks(t *testing.T, s *setupSandbox) []string {
	t.Helper()
	update := updateLockFor(s)
	writeFileT(t, update, "")
	connectLock, err := operationLockPath(s.home, s.getenv)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		lifecycleGatePath(s.home),
		filepath.Join(s.home, setupLockFile),
		connectLock,
		update,
	}
	for _, p := range want {
		if !lexists(p) {
			t.Fatalf("this case is meant to find %s already there, and it is not, so naming it would prove nothing", p)
		}
	}
	return want
}

func TestUninstallNamesEveryLockItLeavesAsSafeToDelete(t *testing.T) {
	for _, purge := range []bool{false, true} {
		name := "plain"
		if purge {
			name = "purge-state"
		}
		t.Run(name, func(t *testing.T) {
			s := installed(t)
			want := allLocks(t, s)

			var code int
			var out, errOut string
			if purge {
				code, out, errOut = s.uninstall(t, tty(s.home), true, "-purge-state", "-home", s.home)
			} else {
				code, out, errOut = s.uninstall(t, nil, false, "-yes", "-home", s.home)
			}
			if code != exitOK {
				t.Fatalf("uninstall: exit %d\n%s\n%s", code, out, errOut)
			}
			// Scoped to the section, not the whole output: -purge-state's
			// own plan prints the full path of everything it removes, so a
			// search over all of it would match the removal line and call
			// that "named as left behind".
			idx := strings.Index(out, leftoverHeading)
			if idx < 0 {
				t.Fatalf("the summary does not name what it left:\n%s", out)
			}
			section := out[idx:]
			// The invariant, either way round: a lock still on disk is
			// named, and one that is gone is not. -purge-state destroys
			// state/, so connect.lock is legitimately absent there and
			// must not be listed.
			for _, p := range want {
				switch named := strings.Contains(section, p); {
				case lexists(p) && !named:
					t.Errorf("the summary does not name %s, which is still there:\n%s", p, out)
				case !lexists(p) && named:
					t.Errorf("the summary names %s, which the run removed:\n%s", p, out)
				}
			}
			// -purge-state destroys state/, and connect.lock goes with it.
			if purge && lexists(want[2]) {
				t.Errorf("-purge-state left %s behind", want[2])
			}
		})
	}
}

// The dry run is the form a participant reads before deciding, so it
// names exactly the locks the real run names at its close. It once listed
// nothing, because sayLeftoverLocks was reached only from closing(); then
// it listed every lock on disk, setup.lock and connect.lock included under
// -purge-state, which removes them, and the binary's update lock under
// -binary, which takes it with the binary. Each mode runs the dry run and
// then the real run on the same installation and compares the two lists.
func TestTheDryRunNamesTheSameLocksTheRealRunWould(t *testing.T) {
	for _, mode := range []string{"plain", "purge-state", "binary"} {
		t.Run(mode, func(t *testing.T) {
			var s *setupSandbox
			var args []string
			switch mode {
			case "plain":
				s = installed(t)
				args = []string{"-yes", "-home", s.home}
			case "purge-state":
				s = installed(t)
				args = []string{"-purge-state", "-home", s.home}
			case "binary":
				posixOnly(t)
				s = newSetupSandbox(t)
				s.exe = filepath.Join(s.home, "bin", binaryNameFor())
				writeFileT(t, s.exe, "the binary")
				if code, out, errOut := s.run(nil, false, "-yes"); code != exitOK {
					t.Fatalf("setup exited %d\n%s\n%s", code, out, errOut)
				}
				args = []string{"-yes", "-binary", "-home", s.home}
			}
			want := allLocks(t, s)

			code, dryOut, errOut := s.uninstall(t, nil, false, append([]string{"-dry-run"}, args...)...)
			if code != exitOK {
				t.Fatalf("dry run: exit %d\n%s\n%s", code, dryOut, errOut)
			}
			// A dry run changes nothing, the locks included.
			for _, p := range want {
				if !lexists(p) {
					t.Errorf("the dry run removed %s", p)
				}
			}
			dry := namedLocks(dryOut, want)

			if mode == "purge-state" {
				code, realOut, errOut := s.uninstall(t, tty(s.home), true, args...)
				if code != exitOK {
					t.Fatalf("uninstall: exit %d\n%s\n%s", code, realOut, errOut)
				}
				checkSameLocks(t, dry, namedLocks(realOut, want), dryOut, realOut)
				return
			}
			code, realOut, errOut := s.uninstall(t, nil, false, args...)
			if code != exitOK {
				t.Fatalf("uninstall: exit %d\n%s\n%s", code, realOut, errOut)
			}
			checkSameLocks(t, dry, namedLocks(realOut, want), dryOut, realOut)
		})
	}
}

// namedLocks is which of the locks the output names as left. Scoped to the
// section: -purge-state's plan prints the full path of everything it
// removes, so a search over the whole output would match a removal line and
// call that "named as left".
func namedLocks(out string, locks []string) []string {
	idx := strings.Index(out, leftoverHeading)
	if idx < 0 {
		return nil
	}
	section := out[idx:]
	var named []string
	for _, p := range locks {
		if strings.Contains(section, "  "+p+"\n") {
			named = append(named, p)
		}
	}
	return named
}

func checkSameLocks(t *testing.T, dry, real []string, dryOut, realOut string) {
	t.Helper()
	if strings.Join(dry, "\n") != strings.Join(real, "\n") {
		t.Errorf("the dry run names %q as left, the real run %q\ndry run:\n%s\nreal run:\n%s", dry, real, dryOut, realOut)
	}
	if len(real) == 0 {
		t.Errorf("the real run names no lock, so the comparison proves nothing:\n%s", realOut)
	}
}

// And it says nothing where there is nothing to say: the section is computed
// from the files present, not printed as a fixed paragraph.
func TestTheDryRunNamesNoLocksWhenThereAreNone(t *testing.T) {
	s := installed(t)
	for _, p := range []string{
		lifecycleGatePath(s.home),
		filepath.Join(s.home, setupLockFile),
		connectLockPath(filepath.Join(s.home, "state")),
	} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if connectLock, err := operationLockPath(s.home, s.getenv); err == nil && connectLock != "" {
		if err := os.Remove(connectLock); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	code, out, errOut := s.uninstall(t, nil, false, "-dry-run", "-home", s.home)
	if code != exitOK {
		t.Fatalf("dry run: exit %d\n%s\n%s", code, out, errOut)
	}
	if strings.Contains(out, leftoverHeading) {
		t.Errorf("the dry run printed the section with no lock file on disk:\n%s", out)
	}
}

// A lock that is gone is not named: uninstall -binary takes the update lock
// with the binary, and a summary that still listed it would send a
// participant looking for a file that is not there.
func TestTheSummaryNamesNoLockThatIsAlreadyGone(t *testing.T) {
	s := installed(t)
	update := updateLockFor(s)
	if lexists(update) {
		t.Fatalf("no upgrade ran in this sandbox and %s exists; the case below would prove nothing", update)
	}
	code, out, errOut := s.uninstall(t, nil, false, "-yes", "-home", s.home)
	if code != exitOK {
		t.Fatalf("uninstall: exit %d\n%s\n%s", code, out, errOut)
	}
	idx := strings.Index(out, leftoverHeading)
	if idx < 0 {
		t.Fatalf("the summary does not name what it left:\n%s", out)
	}
	section := out[idx:]
	if strings.Contains(section, update) {
		t.Errorf("the summary named an update lock that was never there:\n%s", out)
	}
	if !strings.Contains(section, lifecycleGatePath(s.home)) {
		t.Errorf("the gate is still left and must still be named:\n%s", out)
	}
}
