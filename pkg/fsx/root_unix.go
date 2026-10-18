//go:build !windows

package fsx

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// moveDurable renames relative to the root's own directory (renameat), so a
// directory replaced by a link on the way cannot redirect the move, then
// syncs both directories through the root, as MoveFileDurable does through
// their paths.
func (r *Root) moveDurable(from, to string) error {
	if err := r.r.Rename(from, to); err != nil {
		return &StageError{Stage: "move", Err: err}
	}
	for _, dir := range []string{filepath.Dir(to), filepath.Dir(from)} {
		if err := r.syncDir(dir); err != nil {
			return &StageError{Stage: "move directory sync", Published: true, Err: err}
		}
	}
	return nil
}

// writeFile is writeFile's stages, each through the root: an exclusive
// random staging name, a chmod and a sync of the file, then a rename (or,
// exclusive, a link that never replaces) onto name, then a sync of the
// directory.
func (r *Root) writeFile(name string, data []byte, mode fs.FileMode, exclusive bool) error {
	if name == "." || name == "" || !filepath.IsLocal(name) {
		return &StageError{Stage: "name", Err: fs.ErrInvalid}
	}
	tmp, err := stagingName(name)
	if err != nil {
		return &StageError{Stage: "create", Err: err}
	}
	f, err := r.r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return &StageError{Stage: "create", Err: err}
	}
	// The staging name is removed whatever happens: after a rename it is
	// already gone, and after a link it is the second name of the file.
	defer func() { _ = r.r.Remove(tmp) }()
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()
	if err := f.Chmod(mode); err != nil {
		return &StageError{Stage: "chmod", Err: err}
	}
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return &StageError{Stage: "write", Err: err}
	}
	if err := f.Sync(); err != nil {
		return &StageError{Stage: "file sync", Err: err}
	}
	err = f.Close()
	closed = true
	if err != nil {
		return &StageError{Stage: "close", Err: err}
	}
	if exclusive {
		err = r.r.Link(tmp, name)
	} else {
		err = r.r.Rename(tmp, name)
	}
	if err != nil {
		return &StageError{Stage: "publication", Err: err}
	}
	if err := r.syncDir(filepath.Dir(name)); err != nil {
		return &StageError{Stage: "directory sync", Published: true, Err: err}
	}
	return nil
}

func (r *Root) removeDurable(name string) error {
	if err := r.r.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return &StageError{Stage: "remove", Err: err}
	}
	if err := r.syncDir(filepath.Dir(name)); err != nil {
		return &StageError{Stage: "remove directory sync", Published: true, Err: err}
	}
	return nil
}

func (r *Root) syncDir(name string) error {
	f, err := r.openDir(name)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (r *Root) syncDirExported(name string) error { return r.syncDir(name) }

// stagingName is a fresh ".tmp-<random>" name beside name. The prefix is the
// one fsx's path-based writers stage under, which readers already skip.
func stagingName(name string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(name), ".tmp-"+hex.EncodeToString(b[:])), nil
}
