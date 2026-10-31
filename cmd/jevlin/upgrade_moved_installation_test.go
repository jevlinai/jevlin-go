package main

import (
	"os"
	"path/filepath"
	"testing"
)

// After setup moved an earlier installation's key beside a state directory
// outside the installation directory, the check's root is that directory's
// parent; the binary is still this installation's, whose config is in the
// installation directory, and a patch is installed.
func TestAMovedInstallationsCheckStillInstallsAPatch(t *testing.T) {
	f, cfgPath, _ := autoFixture(t, newLocalRelease(t, "0.3.1", map[string]string{"0.3.1": "0.3.1\n"}), "")
	moved := filepath.Join(filepath.Dir(f.home), "elsewhere", "state")
	if err := os.MkdirAll(moved, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, cfgPath, "schema = 1\n\n[search]\nstate_dir = \""+filepath.ToSlash(moved)+"\"\n\n[update]\nmode = \"apply\"\n")
	if code, out, errOut := f.runAuto(cfgPath); code != exitOK || f.content(f.exe) != "0.3.1" {
		t.Errorf("exit %d, binary %s\n%s\n%s", code, f.content(f.exe), out, errOut)
	}
}
