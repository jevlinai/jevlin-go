//go:build windows

package main

import (
	"io/fs"
	"syscall"

	"github.com/jevlinai/jevlin-go/internal/winacl"
)

// systemStateACL reads through internal/winacl; a reparse point is recognized
// by its attribute (isReparsePoint).
func systemStateACL() stateACLBackend {
	return stateACLBackend{
		managed: true,
		read:    winacl.Read,
		user:    winacl.CurrentUser,
		name:    winacl.PrincipalName,
		isLink:  isReparsePoint,
	}
}

// isReparsePoint is the attribute itself rather than the mode Go derives from
// it: a directory junction is neither ModeSymlink nor ModeDir, and a reparse
// point of any kind is what the check refuses to read through.
func isReparsePoint(info fs.FileInfo) bool {
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0
}
