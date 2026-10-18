//go:build windows

package main

import "os"

// writableByOthers has nothing to go on here: Go reports 0777 for every file
// on Windows (see posixModes) and no owner, so only checkBinaryLocation's
// temp-directory rule applies. A directory whose access list lets another
// account write it — a folder made under C:\ rather than the profile, say —
// is accepted. Reading the DACL on the way to the binary is what would close
// that, and it is not done yet.
func writableByOthers(os.FileInfo) (refuse, warn string) { return "", "" }
