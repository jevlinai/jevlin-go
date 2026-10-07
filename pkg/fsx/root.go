package fsx

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Root is a writable root opened for work below its top level (see
// confined.go). A directory inside the root can be replaced by a sandboxed
// command at any time, by a symlink or a junction to somewhere else, so a
// path through it means whatever it means when the kernel walks it. Root
// walks it relative to the root's open directory instead, with os.Root, which
// refuses any link or junction that leads out of the root. A link that stays
// inside gives a sandboxed command nothing it could not already write.
type Root struct {
	dir string
	r   *os.Root
}

// OpenRoot opens dir, bound to the directory that is there now. os.OpenRoot
// follows links in the name it is given, so the directory is Lstat'd first,
// must be a real directory and not a link, and must be the very directory
// the open then reached. Codex's macOS sandbox does not let a command remove
// a writable root (seen on Codex 0.160.0), so this guards a sandbox that
// would, at the cost of two calls.
func OpenRoot(dir string) (*Root, error) {
	before, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, &fs.PathError{Op: "openroot", Path: dir, Err: fmt.Errorf("not a directory (%s)", before.Mode().Type())}
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
	return &Root{dir: dir, r: r}, nil
}

// Close releases the root's directory handle.
func (r *Root) Close() error { return r.r.Close() }

// MkdirAll creates the directory name below the root, and refuses a link
// that would take any part of it out of the root.
func (r *Root) MkdirAll(name string, perm fs.FileMode) error {
	return r.r.MkdirAll(name, perm)
}

// ReadDir lists the directory name below the root, in no particular order.
func (r *Root) ReadDir(name string) ([]fs.DirEntry, error) {
	f, err := r.r.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.ReadDir(-1)
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
