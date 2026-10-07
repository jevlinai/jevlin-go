//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

var (
	errSharingViolation = syscall.Errno(32)
	errLockViolation    = syscall.Errno(33)
)

// errLockNotRegular is a lock path that is a reparse point (a symlink or
// junction) or not a regular disk file.
var errLockNotRegular = errors.New("lock is a reparse point or not a regular file; refusing")

// openLockFile makes the share-mode-0 open that is the lock, without
// following a reparse point, and refuses anything but a regular disk file.
// Lock files live in directories a sandboxed agent can write, so a planted
// link must not steer an unsandboxed open to a path outside them.
func openLockFile(name *uint16, path string, access, disposition uint32) (*os.File, error) {
	h, err := syscall.CreateFile(name, access, 0, nil, disposition,
		syscall.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		_ = syscall.CloseHandle(h)
		return nil, err
	}
	ft, err := syscall.GetFileType(h)
	if err != nil {
		_ = syscall.CloseHandle(h)
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 ||
		ft != windows.FILE_TYPE_DISK {
		_ = syscall.CloseHandle(h)
		return nil, fmt.Errorf("%w: %s", errLockNotRegular, path)
	}
	return os.NewFile(uintptr(h), path), nil
}

// tryLockFile on Windows: an exclusive open (share mode 0) IS the lock,
// and a sharing violation means another flush holds it.
func tryLockFile(path string) (*os.File, bool, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, err
	}
	f, err := openLockFile(name, path, syscall.GENERIC_READ|syscall.GENERIC_WRITE, syscall.OPEN_ALWAYS)
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
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, flushLockReadWrite, err
	}
	mode := flushLockReadWrite
	f, err := openLockFile(name, path, syscall.GENERIC_READ|syscall.GENERIC_WRITE, syscall.OPEN_ALWAYS)
	if err != nil {
		switch classifyFlushLockOpenError(err, false) {
		case flushOpenBusy:
			return nil, false, mode, nil
		case flushOpenFallBack:
		default:
			return nil, false, mode, err
		}
		mode = flushLockReadOnly
		f, err = openLockFile(name, path, syscall.GENERIC_READ, syscall.OPEN_EXISTING)
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
