//go:build windows

package fsx

import (
	"errors"

	"golang.org/x/sys/windows"
)

// heldBriefly reports a rename refused because another handle has the target
// open: what ReplaceFile waits out (replace_file.go). A read-only target
// never gets here; ReplaceFile has already refused it, opening it for
// writing.
func heldBriefly(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
