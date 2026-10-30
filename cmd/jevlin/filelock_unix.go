//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"

	"github.com/jevlinai/jevlin-go/pkg/fsx"
)

// tryLockFile makes one non-blocking attempt to hold path exclusively.
// A lock held elsewhere is (nil, false, nil): the caller decides whether
// that means "wait" or "someone else is already doing this". A flock
// belongs to the open description, so two goroutines contend exactly as
// two processes do.
func tryLockFile(path string) (*os.File, bool, error) {
	// The state dir is a writable root of Codex's sandbox: a link a
	// sandboxed command left at the name is refused, not followed.
	f, err := fsx.OpenLock(path)
	if err != nil {
		return nil, false, err
	}
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
	case err == nil:
		return f, true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		_ = f.Close()
		return nil, false, nil
	default:
		_ = f.Close()
		return nil, false, err
	}
}

func unlockFile(f *os.File) error {
	unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	closeErr := f.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
