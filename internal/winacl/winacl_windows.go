//go:build windows

package winacl

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// CurrentUser is the SID of the account this process runs as.
func CurrentUser() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read the current user: %w", err)
	}
	return user.User.Sid.String(), nil
}

// Read returns path's owner and DACL. Each entry's type, flags and mask come
// from the binary ACE; its trustee comes from the same-index entry of the
// descriptor's string form (SDDLTrustees), which names a SID without the
// pointer arithmetic the binary ACE would need. It reads by name, and a name
// can be a link that reading follows: a caller that must not follow one checks
// first.
func Read(path string) (Descriptor, error) {
	var d Descriptor
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return d, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return d, err
	}
	if owner != nil {
		d.Owner = owner.String()
	}
	control, _, err := sd.Control()
	if err != nil {
		return d, err
	}
	d.Protected = control&windows.SE_DACL_PROTECTED != 0
	dacl, _, err := sd.DACL()
	if err != nil {
		return d, err
	}
	if dacl == nil {
		d.NullDACL = true
		return d, nil
	}
	trustees, err := SDDLTrustees(sd.String())
	if err != nil {
		return d, err
	}
	if len(trustees) != int(dacl.AceCount) {
		return d, fmt.Errorf("%d access entries but %d in the descriptor's string form", dacl.AceCount, len(trustees))
	}
	d.ACEs = make([]ACE, 0, len(trustees))
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return d, err
		}
		sid, err := windows.StringToSid(trustees[i])
		if err != nil {
			return d, fmt.Errorf("entry %d names %q: %w", i, trustees[i], err)
		}
		d.ACEs = append(d.ACEs, ACE{
			Allow: IsAllowACEType(ace.Header.AceType),
			Flags: ace.Header.AceFlags,
			Mask:  uint32(ace.Mask),
			SID:   sid.String(),
		})
	}
	return d, nil
}

// PrincipalName is DOMAIN\name where the SID resolves, else the SID itself. It
// asks the system to look the account up, which can reach a domain controller;
// it is for a report a person asked for, never for a path that decides
// something or that a search waits on.
func PrincipalName(s string) string {
	sid, err := windows.StringToSid(s)
	if err != nil {
		return s
	}
	account, domain, _, err := sid.LookupAccount("")
	switch {
	case err != nil || account == "":
		return s
	case domain == "":
		return account
	}
	return domain + `\` + account
}

// RestrictToOwner makes the current user the object's owner and replaces its
// DACL with one entry granting that user full control, protected from
// inheriting anything else — what `icacls <dir> /setowner <user>
// /inheritance:r /grant:r <user>:(OI)(CI)F` did. A directory's entry is
// inherited by what is created inside it. The owner matters as much as the
// DACL: an owner keeps WRITE_DAC whatever the DACL says, so an object left
// with another owner could be opened up again by them.
func RestrictToOwner(path string, dir bool) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	inherit := uint32(windows.NO_INHERITANCE)
	if dir {
		inherit = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inherit,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user.User.Sid, nil, acl, nil)
}
