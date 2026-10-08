//go:build windows

package winacl

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

const sidBuiltinUsers = "S-1-5-32-545"

// addUsersEntry adds an allow entry for BUILTIN\Users to path's DACL, the way
// a shared parent directory or an agent's sandbox setup does. The list's
// protection is left as it is.
func addUsersEntry(t *testing.T, path string, dir bool) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	users, err := windows.StringToSid(sidBuiltinUsers)
	if err != nil {
		t.Fatal(err)
	}
	inherit := uint32(windows.NO_INHERITANCE)
	if dir {
		inherit = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_READ,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inherit,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(users),
		},
	}}, old)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}

// RestrictToOwner leaves the current user as owner and as the only trustee, in
// a list that inherits nothing, on a directory and on a file. Windows splits a
// directory's one inheritable GENERIC_ALL grant into an effective entry and an
// inherit-only one, both for the user, so the assertion is on who the entries
// are for, not on how many there are.
func TestRestrictToOwnerLeavesTheUserAsOwnerAndOnlyTrustee(t *testing.T) {
	user, err := CurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "restricted")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "secret")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		dir  bool
	}{{dir, true}, {file, false}} {
		// An entry from outside first, so a restriction that only appended the
		// user's own would still leave it.
		addUsersEntry(t, c.path, c.dir)
		before, err := Read(c.path)
		if err != nil {
			t.Fatal(err)
		}
		if !hasEntryFor(before, sidBuiltinUsers) {
			t.Fatalf("fixture: %s has no entry for BUILTIN\\Users before the restriction: %+v", c.path, before)
		}
		if err := RestrictToOwner(c.path, c.dir); err != nil {
			t.Fatal(err)
		}
		after, err := Read(c.path)
		if err != nil {
			t.Fatal(err)
		}
		if after.Owner != user {
			t.Errorf("%s: owner = %s, want %s", c.path, after.Owner, user)
		}
		if !after.Protected || after.NullDACL || len(after.ACEs) == 0 {
			t.Errorf("%s: protected=%v nullDACL=%v entries=%d, want a protected, non-empty list", c.path, after.Protected, after.NullDACL, len(after.ACEs))
		}
		for _, ace := range after.ACEs {
			if !ace.Allow || ace.SID != user {
				t.Errorf("%s: entry %+v, want allow entries for %s only", c.path, ace, user)
			}
		}
	}
}

func hasEntryFor(d Descriptor, sid string) bool {
	for _, ace := range d.ACEs {
		if ace.SID == sid {
			return true
		}
	}
	return false
}

// Read reports an entry for another principal, with its type, as an allow
// entry that carries a mask.
func TestReadReportsAnEntryForAnotherPrincipal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	addUsersEntry(t, dir, true)
	d, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ace := range d.ACEs {
		if ace.SID != sidBuiltinUsers {
			continue
		}
		found = true
		if !ace.Allow || ace.Mask == 0 {
			t.Errorf("the BUILTIN\\Users entry = %+v, want an allow entry with a mask", ace)
		}
	}
	if !found {
		t.Fatalf("no entry for BUILTIN\\Users in %+v", d)
	}
	if d.Owner == "" {
		t.Error("no owner read")
	}
}

// PrincipalName resolves a well-known account and returns anything it cannot
// resolve as it came, so a report always names something.
func TestPrincipalName(t *testing.T) {
	if got := PrincipalName(sidBuiltinUsers); !strings.HasPrefix(got, `BUILTIN\`) {
		t.Errorf("PrincipalName(Users) = %q, want a BUILTIN\\ account", got)
	}
	for _, s := range []string{"S-1-5-21-1-2-3-9999", "not a sid"} {
		if got := PrincipalName(s); got != s {
			t.Errorf("PrincipalName(%q) = %q, want it returned as it came", s, got)
		}
	}
}

// Read does not read through a reparse point: a junction is reported as one,
// whatever its target's list says, and is not an object whose list is
// returned. The target here has no access list, which Read would return as
// NullDACL if it followed the junction. A runner that cannot make a junction
// fails under CI=true instead of skipping, as the repository's permission
// tests do.
func TestReadRefusesAReparsePointAndDoesNotFollowIt(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, nil, nil); err != nil {
		t.Fatalf("fixture: no NULL DACL on the target: %v", err)
	}
	if d, err := Read(target); err != nil || !d.NullDACL {
		t.Fatalf("fixture: the target does not read as a NULL DACL (err %v): %+v", err, d)
	}
	link := filepath.Join(root, "link")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil { // #nosec G204 -- cmd's mklink on this test's own paths
		if os.Getenv("CI") == "true" {
			t.Fatalf("a permission test cannot run on this CI runner, and CI does not let it skip: create a junction: %v: %s", err, out)
		}
		t.Skipf("create a junction: %v: %s", err, out)
	}
	d, err := Read(link)
	if !errors.Is(err, ErrReparsePoint) {
		t.Fatalf("Read(junction) = %+v, %v; want ErrReparsePoint", d, err)
	}
	if d.NullDACL || d.Owner != "" || len(d.ACEs) != 0 {
		t.Errorf("a descriptor came back with the error: %+v", d)
	}
}

// A name that is not there is still reported as absent, through the handle's
// open, so a caller that tells an absent credential from a refused one can.
func TestReadOfAnAbsentNameIsNotExist(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "absent")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Read(absent) err = %v, want fs.ErrNotExist", err)
	}
}
