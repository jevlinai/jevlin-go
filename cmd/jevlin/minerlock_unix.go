//go:build !windows

package main

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"github.com/jevlinai/jevlin-go/pkg/fsx"
)

// tryLockFile makes one non-blocking attempt to hold path exclusively.
// A lock held elsewhere is (nil, false, nil): the caller decides whether
// that means "wait" or "someone else is already doing this". Mirrors the
// refresh-token lock in internal/auth, which is the same mechanism for
// the same reason — a flock belongs to the open description, so two
// goroutines contend exactly as two processes do.
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

// tryFlushLock is tryLockFile with the flush's read-only fallback (see
// flushlock.go): a permission-denied read-write open retries read-only on the
// existing file and takes the same exclusive flock.
func tryFlushLock(path string) (*os.File, bool, flushLockMode, error) {
	// flush.lock sits in the installation's root, which is not a writable
	// root of Codex's sandbox unless a config nests it in one (which agents
	// install refuses to grant); a link at the name is refused either way.
	f, err := fsx.OpenLock(path)
	mode := flushLockReadWrite
	if err != nil {
		if classifyFlushLockOpenError(err, false) != flushOpenFallBack {
			return nil, false, mode, err
		}
		mode = flushLockReadOnly
		f, err = fsx.OpenLockExisting(path)
		if err != nil {
			if classifyFlushLockOpenError(err, true) == flushOpenAbsent {
				return nil, false, mode, errFlushLockAbsent
			}
			return nil, false, mode, err
		}
	}
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
	case err == nil:
		return f, true, mode, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		_ = f.Close()
		return nil, false, mode, nil
	default:
		_ = f.Close()
		return nil, false, mode, err
	}
}

// classifyFlushLockOpenError is the POSIX fallback decision. A read-write open
// denied for permission falls back: EACCES from file modes, and EPERM, which
// is what Codex's macOS sandbox returns. Nothing else does. On the read-only
// retry, a missing file is errFlushLockAbsent. An open never reports a held
// lock here; flock does.
func classifyFlushLockOpenError(err error, readOnly bool) flushOpenOutcome {
	switch {
	case !readOnly && (errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)):
		return flushOpenFallBack
	case readOnly && errors.Is(err, fs.ErrNotExist):
		return flushOpenAbsent
	}
	return flushOpenFailed
}

func unlockFile(f *os.File) error {
	unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	closeErr := f.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
