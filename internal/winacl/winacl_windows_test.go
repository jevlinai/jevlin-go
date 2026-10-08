//go:build windows

package winacl

import (
	"os"
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
