//go:build windows

package main

// doctor's state access check against what Windows actually holds: the
// platform half of state_acl.go (internal/winacl's reader and namer) and the
// line in cmdDoctor that gathers the facts, on a real DACL rather than a fake.
// state_acl_test.go holds the judgment and the verdicts on every OS; this is
// what shows the real reader feeds them.
//
// The second principal is the logon session SID, as in wallet_acl_windows_test.go:
// a group every process of this logon holds and no default access list names,
// granted an inheritable read entry the way a coding agent's sandbox setup
// grants its sandbox group one.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/jevlinai/jevlin-go/internal/winacl"
)

func doctorStateCheckFor(t *testing.T, cfgPath string) map[string]any {
	t.Helper()
	var out, errOut bytes.Buffer
	_ = cmdDoctor([]string{"-config", cfgPath, "-json"}, &out, &errOut)
	return doctorCheckNamed(t, out.String(), "state access")
}

// A state directory setup made is the participant's alone, and doctor says so.
// Once a program grants another principal an entry on it — which is what an
// agent's sandbox is meant to do — doctor names that principal and still says
// OK, because nothing here is wrong; and the store keeps opening, which
// pkg/auth's own test holds.
func TestDoctorStateAccessNamesAnotherPrincipalOnARealDACL(t *testing.T) {
	s := newSetupSandbox(t)
	firstSetup(t, s)
	state := filepath.Join(s.home, "state")

	check := doctorStateCheckFor(t, s.cfgPath())
	detail, _ := check["detail"].(string)
	if check["verdict"] != string(verdictOK) || !strings.Contains(detail, "only you can open") {
		t.Fatalf("a state directory setup made: verdict %v, detail %q; want OK and only you", check["verdict"], detail)
	}

	sid := logonSessionSID(t)
	grantRead(t, state, sid, true)
	if found, _ := entryFor(t, state, sid); !found {
		t.Fatalf("fixture: %s does not carry the entry", state)
	}
	check = doctorStateCheckFor(t, s.cfgPath())
	detail, _ = check["detail"].(string)
	if check["verdict"] != string(verdictOK) {
		t.Errorf("with another principal on the list: verdict %v (%q), want OK", check["verdict"], detail)
	}
	if !strings.Contains(detail, state) || !strings.Contains(detail, winacl.PrincipalName(sid.String())) {
		t.Errorf("the detail does not name the directory and the principal: %q", detail)
	}
	if fix, _ := check["fix"].(string); fix != "" {
		t.Errorf("an informational line carries a fix: %q", fix)
	}
}

// A directory with no access list is open to everyone, which no setup of this
// client produces and no participant would intend: doctor says NO and what to
// do. The directory is one the participant pointed state_dir at, not setup's.
func TestDoctorStateAccessReportsADirectoryWithNoAccessList(t *testing.T) {
	as := newFakeAS(t)
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	setNullDACL(t, stateDir)
	cfgPath := doctorConfig(t, as.srv.URL, stateDir, filepath.Join(root, "spool"))

	check := doctorStateCheckFor(t, cfgPath)
	detail, _ := check["detail"].(string)
	if check["verdict"] != string(verdictNo) || !strings.Contains(detail, "has no access list") {
		t.Fatalf("verdict %v, detail %q; want NO naming the missing list", check["verdict"], detail)
	}
	if fix, _ := check["fix"].(string); !strings.Contains(fix, "access list that only you hold") {
		t.Errorf("fix = %q, want the step that restricts the directory", fix)
	}
}

// setNullDACL gives path no access list at all, which grants everyone
// everything, and proves it did.
func setNullDACL(t *testing.T, path string) {
	t.Helper()
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, nil, nil); err != nil {
		skipPermissionTest(t, "a NULL DACL cannot be set on "+path+" here: "+err.Error())
		return
	}
	if d, err := winacl.Read(path); err != nil || !d.NullDACL {
		t.Fatalf("fixture: %s does not have a NULL DACL (err %v): %+v", path, err, d)
	}
}

// makeJunction makes link a directory junction to target. A junction needs no
// privilege, unlike a symlink, which the Windows runners cannot create without
// Developer Mode: the link cases in state_acl_test.go skip there, so this is
// the platform on which the refusal of a link is actually exercised.
func makeJunction(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil { // #nosec G204 -- cmd's mklink on this test's own paths
		skipPermissionTest(t, fmt.Sprintf("create a directory junction: %v: %s", err, out))
		return
	}
	info, err := os.Lstat(link)
	if err != nil || !isReparsePoint(info) {
		t.Fatalf("fixture: %s is not a reparse point (%v)", link, err)
	}
}

// A junction as the state directory is not read through. Its target has no
// access list, which would be a NO if the junction were followed; the line
// says it could not read the directory instead, and names the link.
func TestDoctorStateAccessDoesNotReadThroughAJunctionAsTheStateDirectory(t *testing.T) {
	as := newFakeAS(t)
	root := t.TempDir()
	target := filepath.Join(root, "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(target, "dpop.key"), "x")
	setNullDACL(t, target)
	stateDir := filepath.Join(root, "state")
	makeJunction(t, stateDir, target)
	cfgPath := doctorConfig(t, as.srv.URL, stateDir, filepath.Join(root, "spool"))

	check := doctorStateCheckFor(t, cfgPath)
	detail, _ := check["detail"].(string)
	if check["verdict"] != string(verdictUnknown) || !strings.Contains(detail, stateDir) || !strings.Contains(detail, "link") {
		t.Fatalf("verdict %v, detail %q; want UNKNOWN naming the junction", check["verdict"], detail)
	}
	if strings.Contains(detail, "no access list") {
		t.Errorf("the junction's target was read through it: %q", detail)
	}
}

// A junction at a credential's name is not read through either, and the rest
// of the directory is still judged: a state directory that is the user's alone
// reads as such for everything but the name that is a link. The target has no
// access list, which would be a NO against dpop.key if it were followed.
func TestDoctorStateAccessDoesNotReadThroughAJunctionAtACredentialName(t *testing.T) {
	as := newFakeAS(t)
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := winacl.RestrictToOwner(stateDir, true); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(stateDir, "agent.json"), "{}")
	target := filepath.Join(root, "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	setNullDACL(t, target)
	link := filepath.Join(stateDir, "dpop.key")
	makeJunction(t, link, target)
	cfgPath := doctorConfig(t, as.srv.URL, stateDir, filepath.Join(root, "spool"))

	check := doctorStateCheckFor(t, cfgPath)
	detail, _ := check["detail"].(string)
	if check["verdict"] != string(verdictUnknown) || !strings.Contains(detail, link) || !strings.Contains(detail, "link") {
		t.Fatalf("verdict %v, detail %q; want UNKNOWN naming %s", check["verdict"], detail, link)
	}
	if strings.Contains(detail, "no access list") {
		t.Errorf("the junction's target was read through it: %q", detail)
	}
}
