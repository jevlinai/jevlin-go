//go:build !windows

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// errLockNotRegular is a lock path that is a symlink or not a regular file.
var errLockNotRegular = errors.New("lock is a symlink or not a regular file; refusing")

// openLockFile opens a lock file without following a final symlink and
// refuses anything but a regular file. Lock files live in directories a
// sandboxed agent can write (the state dir is a Codex writable root), so a
// planted link must not steer an unsandboxed open to a path outside them.
// O_NONBLOCK keeps a planted FIFO from blocking the open; it does not affect
// flock, and Fd() returns the descriptor to blocking mode.
func openLockFile(path string, flag int) (*os.File, error) {
	f, err := os.OpenFile(path, flag|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600) // #nosec G304 -- our own state dir
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) {
			return nil, fmt.Errorf("%w: %s", errLockNotRegular, path)
		}
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s", errLockNotRegular, path)
	}
	return f, nil
}

// tryLockFile makes one non-blocking attempt to hold path exclusively.
// A lock held elsewhere is (nil, false, nil): the caller decides whether
// that means "wait" or "someone else is already doing this". Mirrors the
// refresh-token lock in internal/auth, which is the same mechanism for
// the same reason — a flock belongs to the open description, so two
// goroutines contend exactly as two processes do.
func tryLockFile(path string) (*os.File, bool, error) {
	f, err := openLockFile(path, os.O_RDWR|os.O_CREATE)
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
	f, err := openLockFile(path, os.O_RDWR|os.O_CREATE)
	mode := flushLockReadWrite
	if err != nil {
		if classifyFlushLockOpenError(err, false) != flushOpenFallBack {
			return nil, false, mode, err
		}
		mode = flushLockReadOnly
		f, err = openLockFile(path, os.O_RDONLY)
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
