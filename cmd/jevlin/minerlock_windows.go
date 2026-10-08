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
// and a sharing violation means another flush holds it.
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

// tryFlushLock is tryLockFile with the flush's read-only fallback (see
// flushlock.go): an access-denied read-write open retries as a share-mode-0
// read handle on the existing file, which is the same exclusive lock.
func tryFlushLock(path string) (*os.File, bool, flushLockMode, error) {
	mode := flushLockReadWrite
	// Share mode 0 is the lock; fsx refuses a link or junction at the name.
	f, err := fsx.OpenLock(path)
	if err != nil {
		switch classifyFlushLockOpenError(err, false) {
		case flushOpenBusy:
			return nil, false, mode, nil
		case flushOpenFallBack:
		default:
			return nil, false, mode, err
		}
		mode = flushLockReadOnly
		f, err = fsx.OpenLockExisting(path)
		if err != nil {
			switch classifyFlushLockOpenError(err, true) {
			case flushOpenBusy:
				return nil, false, mode, nil
			case flushOpenAbsent:
				return nil, false, mode, errFlushLockAbsent
			default:
				return nil, false, mode, err
			}
		}
	}
	return f, true, mode, nil
}

// classifyFlushLockOpenError is the Windows fallback decision. The exclusive
// open is the lock, so a sharing or lock violation is busy on either open. A
// read-write open denied with ERROR_ACCESS_DENIED falls back; on the
// read-only retry, ERROR_FILE_NOT_FOUND is errFlushLockAbsent. Nothing else
// falls back.
func classifyFlushLockOpenError(err error, readOnly bool) flushOpenOutcome {
	switch {
	case errors.Is(err, errSharingViolation) || errors.Is(err, errLockViolation):
		return flushOpenBusy
	case !readOnly && errors.Is(err, syscall.ERROR_ACCESS_DENIED):
		return flushOpenFallBack
	case readOnly && errors.Is(err, syscall.ERROR_FILE_NOT_FOUND):
		return flushOpenAbsent
	}
	return flushOpenFailed
}

func unlockFile(f *os.File) error { return f.Close() }
