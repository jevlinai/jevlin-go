//go:build !windows

package auth

// posixModes: file modes carry the owner/group/world bits this package
// checks. On Windows they do not — Go reports 0777 for every directory —
// so access control there is the DACL perm_windows.go sets and verifies.
const posixModes = true

// restrictStateDir: the 0700 mode OpenStore creates with is the whole story.
func restrictStateDir(string) error { return nil }

// checkStateAccess: the mode checks in store.go are the whole story.
func checkStateAccess(string) error { return nil }
