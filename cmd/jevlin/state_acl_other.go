//go:build !windows

package main

import "io/fs"

// systemStateACL: file modes are the whole story here (pkg/auth/perm.go), so
// the state directory's access lists are not managed and doctor has no state
// access check. isLink is still the POSIX meaning, for a test that substitutes a
// managed backend.
func systemStateACL() stateACLBackend {
	return stateACLBackend{
		isLink: func(info fs.FileInfo) bool { return info.Mode()&fs.ModeSymlink != 0 },
	}
}
