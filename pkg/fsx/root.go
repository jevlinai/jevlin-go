package fsx

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Root is a writable root opened so that a path inside it means what it
// meant when the root was opened (see confined.go). A directory inside the
// root can be replaced by a sandboxed command at any time, by a symlink or a
// junction to somewhere else, so a path through it means whatever it means
// when the kernel walks it. Root walks it relative to the root's open
// directory instead, with os.Root, which refuses any link or junction that
// leads out of the root. A link that stays inside gives a sandboxed command
// nothing it could not already write.
//
// A root can itself sit inside another writable root: the spool's default is
// <state_dir>/spool, and the state dir is always one. A sandboxed command can
// rename such a root aside and put a link in its place, so a caller that
// keeps working in it holds the identity it opened (Identity, SameDirectory)
// and opens it again bound to that identity, never trusting the path twice.
type Root struct {
	dir string
	r   *os.Root
	id  fs.FileInfo
}

// OpenRoot opens dir, bound to the directory that is there now. os.OpenRoot
// follows links in the name it is given, so the directory is Lstat'd first,
// must be a real directory and not a link, and must be the very directory
// the open then reached. Live on Codex 0.160.0's macOS sandbox, a command
// that emptied a writable root (unlink of each entry) could not rmdir it, so
// a top-level root cannot be swapped; a root inside another writable root
// can, by a rename, and this is what refuses the link left in its place.
func OpenRoot(dir string) (*Root, error) {
	before, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, &fs.PathError{Op: "openroot", Path: dir, Err: fmt.Errorf("not a directory (%s); name the real directory, not a link to it", before.Mode().Type())}
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	opened, err := r.Stat(".")
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	if !os.SameFile(before, opened) {
		_ = r.Close()
		return nil, &fs.PathError{Op: "openroot", Path: dir, Err: fmt.Errorf("replaced while it was opened")}
	}
	return &Root{dir: dir, r: r, id: opened}, nil
}

// Close releases the root's directory handle.
func (r *Root) Close() error { return r.r.Close() }

// Identity is the directory this root was opened on, for SameDirectory.
func (r *Root) Identity() fs.FileInfo { return r.id }

// SameDirectory reports whether this root was opened on the directory id
// names: a caller that opened a root once and opens it again by path uses it
// to refuse a directory swapped in at that path in between.
func (r *Root) SameDirectory(id fs.FileInfo) bool { return id != nil && os.SameFile(r.id, id) }

// MkdirAll creates the directory name below the root, and refuses a link
// that would take any part of it out of the root.
func (r *Root) MkdirAll(name string, perm fs.FileMode) error {
	return r.r.MkdirAll(name, perm)
}

// ReadDir lists the directory name below the root ("." for the root itself),
// in no particular order. The open does not wait, so a FIFO left at the name
// is refused rather than waited on.
func (r *Root) ReadDir(name string) ([]fs.DirEntry, error) {
	f, err := r.openDir(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.ReadDir(-1)
}

// openDir opens a directory below the root without waiting, and refuses
// anything that is not one.
func (r *Root) openDir(name string) (*os.File, error) {
	f, err := r.r.OpenFile(name, os.O_RDONLY|noWait, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && !info.IsDir() {
		err = &fs.PathError{Op: "open", Path: filepath.Join(r.dir, name), Err: syscall.ENOTDIR}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// ReadRegular is the package's ReadRegular for a name below the root.
func (r *Root) ReadRegular(name string, limit int64) ([]byte, error) {
	f, err := r.r.OpenFile(name, os.O_RDONLY|noWait, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readBounded(f, filepath.Join(r.dir, name), limit)
}

// MoveDurable moves the file from to to, both names relative to the root,
// with MoveFileDurable's semantics: a *StageError, Published when the file is
// at to but the move's durability is not confirmed.
func (r *Root) MoveDurable(from, to string) error {
	return r.moveDurable(from, to)
}

// WriteFileAtomic is the package's WriteFileAtomic for a name relative to the
// root: staged under an exclusive random name, synced, renamed into place,
// and the directory synced, all through the root.
func (r *Root) WriteFileAtomic(name string, data []byte, mode fs.FileMode) error {
	return r.writeFile(name, data, mode, false)
}

// WriteFileExclusive is the package's WriteFileExclusive for a name relative
// to the root: published only if absent, an existing winner matching
// fs.ErrExist.
func (r *Root) WriteFileExclusive(name string, data []byte, mode fs.FileMode) error {
	return r.writeFile(name, data, mode, true)
}

// RemoveDurable is the package's RemoveFileDurable for a name relative to the
// root.
func (r *Root) RemoveDurable(name string) error {
	return r.removeDurable(name)
}

// SyncDir makes a mutation of the directory name below the root durable,
// like SyncDirectory; on Windows it reports ErrDirectorySyncUnsupported, as
// SyncDirectory does, because publication there is write-through.
func (r *Root) SyncDir(name string) error { return r.syncDirExported(name) }
