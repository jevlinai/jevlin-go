package fsx

// Directories a sandboxed agent can also write.
//
// Codex's workspace-write sandbox is given this client's state directory, and
// with mining its intake, sessions and spool directories, as writable roots.
// This process often runs outside that sandbox (a hook, a flush a hook
// started, connect's resume) and works in the same directories, so whatever a
// sandboxed command leaves at a name there is input, not this process's own
// file. Live, on Codex 0.160.0's macOS sandbox, a command in a writable root
// could create a symlink pointing out of it, a hard link to a file outside it
// that the participant owns, and a FIFO; it could not remove the root itself.
//
// So a write by this process must never go where such a name points, and a
// read must never wait on one. Each function here holds one rule:
//
//   - CreateNew never opens an existing name for writing, whatever it is: a
//     regular file, a symlink, or a hard link to a file elsewhere. Replacing a
//     file is CreateNew under a fresh unpredictable name, then a rename onto
//     the final name, and a rename replaces the name, never what it named.
//   - ReadRegular never waits on anything but a regular file, and never reads
//     past a bound.
//   - OpenLock never follows a link and takes only a regular file. A hard link
//     at the name is locked, never written: the lock is taken, not the bytes.
//
// Writing directly inside a writable root needs nothing more, because the
// root itself cannot be replaced from inside. A path that goes through a
// directory below it can be redirected by replacing that directory, so that
// goes through Root.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// ErrNotRegular is a name that holds something other than a regular file.
var ErrNotRegular = errors.New("fsx: not a regular file")

// ErrTooLarge is a file larger than the caller's bound.
var ErrTooLarge = errors.New("fsx: file larger than its bound")

// CreateNew writes data to a new file at path. It fails, matching
// fs.ErrExist, when anything already has that name, so no existing file is
// ever opened for writing. A file it created and could not finish is removed.
func CreateNew(path string, data []byte, mode fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) // #nosec G304 -- the caller's own directory; O_EXCL never opens an existing name
	if err != nil {
		return err
	}
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path) // ours: O_EXCL created it
		return err
	}
	return nil
}

// ReadRegular reads the file at path, refusing anything that is not a regular
// file or is larger than limit bytes. The open does not wait: a FIFO at the
// name opens at once and is then refused (see openNoWait).
func ReadRegular(path string, limit int64) ([]byte, error) {
	f, err := openNoWait(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readBounded(f, path, limit)
}

// readBounded is ReadRegular's check and read on an open file.
func readBounded(f *os.File, path string, limit int64) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, &fs.PathError{Op: "read", Path: path, Err: ErrNotRegular}
	}
	if info.Size() > limit {
		return nil, &fs.PathError{Op: "read", Path: path, Err: ErrTooLarge}
	}
	// A file that grows after the stat is still held to the bound.
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, &fs.PathError{Op: "read", Path: path, Err: ErrTooLarge}
	}
	return data, nil
}

// ReadRegularNoFollow is ReadRegular for a name that must not be a link. The
// open itself refuses one (O_NOFOLLOW; on Windows, a reparse point), so a
// check made on the name beforehand cannot be outrun by replacing it between
// the check and the read. It returns the open file's own information, for any
// further check (owner-only mode, say) to be made on what was read rather
// than on what the name was.
func ReadRegularNoFollow(path string, limit int64) ([]byte, fs.FileInfo, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	data, err := readBounded(f, path, limit)
	if err != nil {
		return nil, nil, err
	}
	return data, info, nil
}

// OpenLock opens path for an exclusive lock, creating it when absent. A
// symlink at the name is refused rather than followed, and anything that is
// not a regular file is refused once open. The open error is returned as the
// system gave it, so a caller can still tell a busy lock or a denied open
// from any other failure.
func OpenLock(path string) (*os.File, error) {
	f, err := openLock(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, &fs.PathError{Op: "open", Path: path, Err: fmt.Errorf("lock: %w", ErrNotRegular)}
	}
	return f, nil
}
