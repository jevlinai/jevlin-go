//go:build windows

package main

import "github.com/jevlinai/jevlin-go/internal/winacl"

// systemStateACL reads through internal/winacl, the same reader the wallet check
// uses; a reparse point is recognized as wallet_acl_windows.go recognizes one.
func systemStateACL() stateACLBackend {
	return stateACLBackend{
		managed: true,
		read:    winacl.Read,
		user:    winacl.CurrentUser,
		name:    winacl.PrincipalName,
		isLink:  isReparsePoint,
	}
}
