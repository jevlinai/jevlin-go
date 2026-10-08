//go:build windows

package winacl

import (
	"fmt"
	"io/fs"

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
// pointer arithmetic the binary ACE would need.
//
// It opens the object once, without following a reparse point, refuses it
// (ErrReparsePoint) if the handle's own attributes say it is one, and reads the
// descriptor from that same handle. Reading by name instead, after a check on
// the name, leaves a gap in which the name can be replaced: a junction or a
// hard link would have the caller report another object's list under this
// object's name, and a symlink could send the read to a network path. One
// handle has no gap, because what was checked is what is read.
func Read(path string) (Descriptor, error) {
	var d Descriptor
	h, err := openNoFollow(path)
	if err != nil {
		return d, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return d, &fs.PathError{Op: "read the access list of", Path: path, Err: err}
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

// openNoFollow opens path for reading its security descriptor and attributes
// and nothing else: READ_CONTROL for the descriptor, FILE_READ_ATTRIBUTES so
// the handle can be asked what it is. FILE_FLAG_OPEN_REPARSE_POINT opens a
// symlink or junction at the name as itself instead of following it, and the
// attribute check on the handle then refuses it. FILE_FLAG_BACKUP_SEMANTICS is
// what lets a directory be opened at all. The share mode admits every other
// opener, so this never makes a writer wait.
func openNoFollow(path string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(name,
		windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0)
	if err != nil {
		return 0, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		_ = windows.CloseHandle(h)
		return 0, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(h)
		return 0, &fs.PathError{Op: "open", Path: path, Err: ErrReparsePoint}
	}
	return h, nil
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
