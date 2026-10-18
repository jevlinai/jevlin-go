//go:build !windows

package fsx

import (
	"os"
	"syscall"
)

// noWait is the open flag that keeps a read from blocking on a FIFO. Opening
// a FIFO for reading with O_NONBLOCK returns at once even with no writer, and
// the regular-file check that follows refuses it. On a regular file it
// changes nothing.
const noWait = syscall.O_NONBLOCK

// openNoWait opens path for reading without waiting on a FIFO.
func openNoWait(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|noWait, 0) // #nosec G304 -- the caller's own directory; the result is checked to be a regular file before a byte is read
}

// openNoFollow opens path for reading, refusing a symlink at the name and
// without waiting on a FIFO.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|noWait, 0) // #nosec G304 -- the caller's own directory; a link is refused, not followed
}

// openLock opens or creates path for locking, refusing a symlink at the name
// (O_NOFOLLOW) and without waiting on a FIFO. O_TRUNC is never set, so a hard
// link at the name is opened and locked, never written.
func openLock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|noWait, 0o600) // #nosec G304 -- the caller's own directory; a link is refused, not followed
}

// openLockExisting opens an existing lock read-only, refusing a symlink at
// the name and without waiting on a FIFO.
func openLockExisting(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|noWait, 0) // #nosec G304 -- the caller's own directory; a link is refused, not followed
}
