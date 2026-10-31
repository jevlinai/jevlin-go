//go:build windows

package main

import (
	"errors"
	"os"
	"syscall"

	"github.com/jevlinai/jevlin-go/pkg/fsx"
)

var (
	errSharingViolation = syscall.Errno(32)
	errLockViolation    = syscall.Errno(33)
)

// tryLockFile on Windows: an exclusive open (share mode 0) IS the lock,
// and a sharing violation means another process holds it.
func tryLockFile(path string) (*os.File, bool, error) {
	// Share mode 0, and the state dir being a writable root of Codex's
	// sandbox, a link at the name refused rather than followed.
	f, err := fsx.OpenLock(path)
	if err != nil {
		if errors.Is(err, errSharingViolation) || errors.Is(err, errLockViolation) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return f, true, nil
}

func unlockFile(f *os.File) error { return f.Close() }
