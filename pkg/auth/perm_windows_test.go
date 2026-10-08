//go:build windows

package auth

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/jevlinai/jevlin-go/internal/winacl"
)

const sidBuiltinUsers = "S-1-5-32-545"

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

func hasEntryFor(d winacl.Descriptor, sid string) bool {
	for _, ace := range d.ACEs {
		if ace.SID == sid {
			return true
		}
	}
	return false
}

// OpenStore gives a directory it creates a protected DACL whose entries are
// all for the current user, so nothing is inherited from the parent. The
// fixture proves itself first: a directory made beside it by plain Mkdir does
// inherit the parent's BUILTIN\Users entry, which is the hole.
func TestOpenStoreCreatesOwnerOnlyDACL(t *testing.T) {
	parent := t.TempDir()
	grantUsersRead(t, parent, true)

	plain := filepath.Join(parent, "plain")
	if err := os.Mkdir(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	if d, err := winacl.Read(plain); err != nil || !hasEntryFor(d, sidBuiltinUsers) {
		t.Fatalf("fixture: a directory made by Mkdir under the parent does not inherit its BUILTIN\\Users entry (err %v): %+v", err, d)
	}

	dir := filepath.Join(parent, "state")
	if _, err := OpenStore(dir); err != nil {
		t.Fatal(err)
	}
	user, err := winacl.CurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	d, err := winacl.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Protected {
		t.Errorf("state dir DACL is not protected: %+v", d)
	}
	// Windows splits the one inheritable GENERIC_ALL grant into an effective
	// FA entry and an inherit-only GA entry, both for the user, so the
	// assertion is on who the entries are for rather than on how many there are.
	if d.NullDACL || len(d.ACEs) == 0 {
		t.Fatalf("state dir DACL = %+v, want entries for %s only", d, user)
	}
	for _, ace := range d.ACEs {
		if !ace.Allow || ace.SID != user {
			t.Fatalf("state dir DACL entry %+v, want allow entries for %s only", ace, user)
		}
	}
}

// The state directory is meant to be reachable by an agent's sandbox, whose
// setup grants its own group an entry on it. Another principal's entry on the
// directory or on a secret therefore refuses nothing: not an open, not a load.
// The test adds the entry the way that setup does, after the store has made
// its secrets, and then goes through every way in.
func TestAnotherPrincipalsEntryRefusesNothingOnLoad(t *testing.T) {
	s, dir := newStore(t)
	if _, err := s.DPoPKey(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRefreshToken("refresh-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ParticipationSecret(); err != nil {
		t.Fatal(err)
	}

	grantUsersRead(t, dir, true)
	for _, name := range []string{dpopKeyFile, refreshTokenFile, participationSecretFile} {
		path := filepath.Join(dir, name)
		grantUsersRead(t, path, false)
		d, err := winacl.Read(path)
		if err != nil || !hasEntryFor(d, sidBuiltinUsers) {
			t.Fatalf("fixture: %s does not carry the BUILTIN\\Users entry (err %v): %+v", name, err, d)
		}
	}

	if _, err := OpenStoreExisting(dir); err != nil {
		t.Errorf("OpenStoreExisting refused a state dir another principal can open: %v", err)
	}
	if _, err := OpenStore(dir); err != nil {
		t.Errorf("OpenStore refused a state dir another principal can open: %v", err)
	}
	if _, err := s.DPoPKeyExisting(); err != nil {
		t.Errorf("dpop.key refused: %v", err)
	}
	if tok, ok, err := s.LoadRefreshToken(); err != nil || !ok || tok != "refresh-token" {
		t.Errorf("refresh.token = %q, %v, %v; want it loaded", tok, ok, err)
	}
	if _, err := s.ParticipationSecret(); err != nil {
		t.Errorf("participation.secret refused: %v", err)
	}
}
