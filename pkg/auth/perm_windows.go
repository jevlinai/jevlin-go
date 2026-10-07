//go:build windows

package auth

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// posixModes is false on Windows: Go reports 0777 for every directory
// and file regardless of ACLs, so a mode check would refuse every store.
// Custody there rests on the DACL restrictStateDir sets and
// checkStateAccess verifies (perm.go).
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

// checkStateAccess reads path's owner and DACL and judges them. The caller
// has already refused a symlink: reading a DACL by name follows one.
func checkStateAccess(path string) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	acc, err := readStateAccess(path)
	if err != nil {
		return fmt.Errorf("read access list: %w", err)
	}
	return judgeStateAccess(acc, sid.String(), principalName)
}

// readStateAccess takes each entry's type from the binary ACE and its trustee
// from the same-index entry of the descriptor's string form, as the wallet's
// inspectWindowsWalletObject does: naming the SID from the binary ACE would
// need unsafe, which the module admits in one file only.
func readStateAccess(path string) (stateAccess, error) {
	var acc stateAccess
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return acc, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return acc, err
	}
	if owner != nil {
		acc.owner = owner.String()
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return acc, err
	}
	if dacl == nil {
		acc.nullDACL = true
		return acc, nil
	}
	trustees, err := sddlTrustees(sd.String())
	if err != nil {
		return acc, err
	}
	if len(trustees) != int(dacl.AceCount) {
		return acc, fmt.Errorf("%d access entries but %d in the descriptor's string form", dacl.AceCount, len(trustees))
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return acc, err
		}
		s, err := windows.StringToSid(trustees[i])
		if err != nil {
			return acc, fmt.Errorf("entry %d names %q: %w", i, trustees[i], err)
		}
		acc.aces = append(acc.aces, stateACE{allow: isAllowACEType(ace.Header.AceType), sid: s.String()})
	}
	return acc, nil
}

// isAllowACEType: the four allow entry types a DACL can hold (plain, object,
// callback, callback object).
func isAllowACEType(t uint8) bool {
	switch t {
	case 0x0, 0x5, 0x9, 0xB:
		return true
	}
	return false
}

// sddlTrustees returns the trustee field of each ACE in a descriptor string's
// DACL, in order: "O:LAD:PAI(A;;FA;;;LA)(A;OICIIO;GA;;;LA)" -> [LA LA]. An
// owner is an alias or an S-1-… string, never containing ':', so the first
// "D:" starts the DACL. A conditional entry carries parentheses of its own, so
// an entry ends at the parenthesis that closes it.
func sddlTrustees(sddl string) ([]string, error) {
	start := strings.Index(sddl, "D:")
	if start < 0 {
		return nil, errors.New("the descriptor has no DACL")
	}
	var trustees []string
	depth, open := 0, -1
	for i := start + 2; i < len(sddl); i++ {
		switch sddl[i] {
		case '(':
			if depth == 0 {
				open = i
			}
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced access entry in %q", sddl)
			}
			if depth == 0 {
				fields := strings.SplitN(sddl[open+1:i], ";", 7)
				if len(fields) < 6 {
					return nil, fmt.Errorf("unexpected access entry %q", sddl[open:i+1])
				}
				trustees = append(trustees, fields[5])
			}
		default:
			if depth == 0 && i+1 < len(sddl) && sddl[i+1] == ':' && open >= 0 {
				return trustees, nil
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unterminated access entry in %q", sddl)
	}
	return trustees, nil
}

// principalName is DOMAIN\name (SID) where the SID resolves, else the SID.
func principalName(s string) string {
	sid, err := windows.StringToSid(s)
	if err != nil {
		return s
	}
	account, domain, _, err := sid.LookupAccount("")
	switch {
	case err != nil || account == "":
		return s
	case domain == "":
		return account + " (" + s + ")"
	}
	return domain + `\` + account + " (" + s + ")"
}
