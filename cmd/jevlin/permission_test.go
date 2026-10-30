package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// requireSandboxEmulation leaves a test that needs a write-denying sandbox
// emulated (denyWritesKeepReads) when this process cannot emulate one.
func requireSandboxEmulation(t *testing.T) {
	t.Helper()
	if reason := sandboxEmulationUnavailable(); reason != "" {
		skipPermissionTest(t, reason)
	}
}

// permissionTestTB is the part of testing.TB skipPermissionTest uses, so its
// own test can watch it decide without being stopped by it.
type permissionTestTB interface {
	Helper()
	Skip(args ...any)
	Fatalf(format string, args ...any)
}

// skipPermissionTest is the one way a permission test leaves when this
// environment cannot establish its condition (root on POSIX, an ACL Windows
// will not set, no symbolic links). Locally it skips with the reason. Under
// CI=true, which GitHub Actions sets, it fails instead: CI runs go test
// without -v, so a skip there would read as a pass, and a green job must mean
// the test ran.
func skipPermissionTest(t permissionTestTB, reason string) {
	t.Helper()
	if os.Getenv("CI") == "true" {
		t.Fatalf("a permission test cannot run on this CI runner, and CI does not let it skip: %s", reason)
		return
	}
	t.Skip(reason)
}

type recordingPermissionTB struct {
	skipped, failed bool
	message         string
}

func (r *recordingPermissionTB) Helper() {}
func (r *recordingPermissionTB) Skip(args ...any) {
	r.skipped, r.message = true, fmt.Sprint(args...)
}
func (r *recordingPermissionTB) Fatalf(format string, args ...any) {
	r.failed, r.message = true, fmt.Sprintf(format, args...)
}

func TestAPermissionTestFailsInsteadOfSkippingUnderCI(t *testing.T) {
	const reason = "running as root: file modes do not deny root"
	t.Setenv("CI", "true")
	var ci recordingPermissionTB
	skipPermissionTest(&ci, reason)
	if !ci.failed || ci.skipped || !strings.Contains(ci.message, reason) {
		t.Errorf("under CI=true: failed %v, skipped %v, message %q; want a failure naming the reason", ci.failed, ci.skipped, ci.message)
	}

	t.Setenv("CI", "")
	var local recordingPermissionTB
	skipPermissionTest(&local, reason)
	if local.failed || !local.skipped || local.message != reason {
		t.Errorf("outside CI: failed %v, skipped %v, message %q; want a skip with the reason", local.failed, local.skipped, local.message)
	}
}
