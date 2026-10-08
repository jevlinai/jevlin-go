//go:build windows

package auth

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// posixModes is false on Windows: Go reports 0777 for every directory
// and file regardless of ACLs, so a mode check would refuse every store.
// Custody there rests on the DACL restrictStateDir sets when OpenStore creates
// the directory (perm.go).
const posixModes = false

func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read the current user: %w", err)
	}
	return user.User.Sid, nil
}

// restrictStateDir gives dir a protected owner-only DACL whose single entry
// every file and directory created beneath it inherits — the same access list
// jevlin setup's restrictToOwner applies to <home>/state.
func restrictStateDir(dir string) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
}
