//go:build !windows

package fsx

import (
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

func (r *Root) syncDir(name string) error {
	f, err := r.r.Open(name)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
