//go:build windows

package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// grantUsersRead adds a read entry for BUILTIN\Users to path, the way a
// sandbox setup or a shared parent directory would; inheritable on a directory.
func grantUsersRead(t *testing.T, path string, dir bool) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	users, err := windows.StringToSid("S-1-5-32-545")
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

// OpenStore gives a directory it creates a protected DACL with one entry,
// for the current user, so nothing is inherited from the parent.
func TestOpenStoreCreatesOwnerOnlyDACL(t *testing.T) {
	parent := t.TempDir()
	grantUsersRead(t, parent, true)
	dir := filepath.Join(parent, "state")
	if _, err := OpenStore(dir); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("state dir DACL is not protected: %s", sd)
	}
	acc, err := readStateAccess(dir)
	if err != nil {
		t.Fatal(err)
	}
	user, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if len(acc.aces) != 1 || !acc.aces[0].allow || acc.aces[0].sid != user.String() {
		t.Fatalf("state dir DACL = %s, want one entry for %s", sd, user)
	}
}

// A state dir that admits another principal is refused on open, and a
// secret that does is refused on load.
func TestForeignAccessRefused(t *testing.T) {
	s, dir := newStore(t)
	if _, err := s.DPoPKey(); err != nil {
		t.Fatal(err)
	}
	grantUsersRead(t, filepath.Join(dir, dpopKeyFile), false)
	if _, err := s.DPoPKey(); err == nil || !strings.Contains(err.Error(), "grants access to") {
		t.Fatalf("dpop.key readable by Users not refused: %v", err)
	}

	other := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStoreExisting(other); err != nil {
		t.Fatalf("a profile-inherited state dir is refused: %v", err)
	}
	grantUsersRead(t, other, true)
	if _, err := OpenStoreExisting(other); err == nil || !strings.Contains(err.Error(), "grants access to") {
		t.Fatalf("state dir readable by Users not refused: %v", err)
	}
	if _, err := OpenStore(other); err == nil {
		t.Fatal("OpenStore accepted an existing state dir readable by Users")
	}
}
