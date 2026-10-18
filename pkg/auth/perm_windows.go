//go:build windows

package auth

import "github.com/jevlinai/jevlin-go/internal/winacl"

// posixModes is false on Windows: Go reports 0777 for every directory
// and file regardless of ACLs, so a mode check would refuse every store.
// Custody there rests on the DACL restrictStateDir sets when OpenStore creates
// the directory (perm.go).
const posixModes = false

// restrictStateDir makes the current user the directory's owner and gives it a
// protected DACL whose single entry every file and directory created beneath it
// inherits: exactly what jevlin setup applies to <home>\state, because it is the
// same function. The owner is part of it. An owner keeps WRITE_DAC whatever the
// list says, and the owner a directory is born with is the creating process's
// default one (BUILTIN\Administrators when elevated), so a list alone would
// leave the directory re-openable by a principal the list does not name.
func restrictStateDir(dir string) error { return winacl.RestrictToOwner(dir, true) }
