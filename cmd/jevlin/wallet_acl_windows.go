//go:build windows

package main

// The Windows half of wallet_acl.go: read an object's DACL, give it a protected
// owner-only one, recognize a reparse point. Reading and setting are
// internal/winacl, which pkg/auth uses for the state directory too.

import (
	"io/fs"
	"syscall"

	"github.com/jevlinai/jevlin-go/internal/winacl"
)

func systemWalletACL() walletACLBackend {
	return walletACLBackend{
		managed: true,
		inspect: inspectWindowsWalletObject,
		protect: restrictToOwner,
		isLink:  isReparsePoint,
	}
}

// isReparsePoint is the attribute itself rather than the mode Go derives from
// it: a directory junction is neither ModeSymlink nor ModeDir, and a reparse
// point of any kind is what setup refuses to follow.
func isReparsePoint(info fs.FileInfo) bool {
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0
}

// inspectWindowsWalletObject reads path's DACL (winacl.Read) and judges it. A
// NULL DACL grants everyone everything.
func inspectWindowsWalletObject(path string) (walletObjectAccess, error) {
	var acc walletObjectAccess
	owner, err := winacl.CurrentUser()
	if err != nil {
		return acc, err
	}
	d, err := winacl.Read(path)
	if err != nil {
		return acc, err
	}
	acc.protected = d.Protected
	if d.NullDACL {
		acc.readers = []string{"everyone (the object has no access list)"}
		return acc, nil
	}
	aces := make([]walletACE, 0, len(d.ACEs))
	for _, ace := range d.ACEs {
		aces = append(aces, walletACE{allow: ace.Allow, flags: ace.Flags, mask: ace.Mask, sid: ace.SID})
	}
	var sids []string
	acc.ownerOnly, sids = judgeWalletACEs(aces, owner)
	for _, s := range sids {
		acc.readers = append(acc.readers, winacl.PrincipalName(s))
	}
	return acc, nil
}
