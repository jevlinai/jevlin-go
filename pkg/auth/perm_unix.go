//go:build !windows

package auth

// posixModes: file modes carry the owner/group/world bits this package
// checks. On Windows they do not — Go reports 0777 for every directory —
// so access control there is the DACL perm_windows.go sets when OpenStore
// creates the directory, and that nothing here reads back (perm.go).
const posixModes = true

// restrictStateDir: the 0700 mode OpenStore creates with is the whole story.
func restrictStateDir(string) error { return nil }
