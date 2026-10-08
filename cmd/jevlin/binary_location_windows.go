//go:build windows

package main

import "os"

// writableByOthers has nothing to go on here: Go reports 0777 for every file
// on Windows (see posixModes), so only the temp-directory rule applies.
func writableByOthers(os.FileInfo) (refuse, warn string) { return "", "" }
