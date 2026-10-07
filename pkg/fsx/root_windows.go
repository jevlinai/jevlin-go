package fsx

import (
	"fmt"
	"io/fs"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// On Windows every operation through a Root keeps the durability barrier the
// path-based writers use, MOVEFILE_WRITE_THROUGH, which os.Root's rename does
// not set. MoveFileEx takes paths, so each directory the operation goes
// through is pinned first, the root's own included (a root inside another
// writable root can be renamed): opened by its path without
// FILE_SHARE_DELETE, which stops anyone renaming or deleting it while the
// handle is held, and checked to be a real directory, not a reparse point,
// that is the same directory the root resolves at that name. The paths then
// name those directories for the whole operation, and the path-based code
// does the rest.

// pinDirs pins "." and each distinct directory of names, and returns a
// function that releases them.
func (r *Root) pinDirs(names ...string) (func(), error) {
	var pins []windows.Handle
	release := func() {
		for _, h := range pins {
			_ = windows.CloseHandle(h)
		}
	}
	seen := map[string]bool{}
	for _, dir := range append([]string{"."}, names...) {
		dir = filepath.Clean(dir)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		h, err := r.pinDir(dir)
		if err != nil {
			release()
			return nil, err
		}
		pins = append(pins, h)
	}
	return release, nil
}

func (r *Root) moveDurable(from, to string) error {
	release, err := r.pinDirs(filepath.Dir(to), filepath.Dir(from))
	if err != nil {
		return &StageError{Stage: "move", Err: err}
	}
	defer release()
	if err := movePublication(filepath.Join(r.dir, from), filepath.Join(r.dir, to)); err != nil {
		return &StageError{Stage: "move", Err: err}
	}
	return nil
}

func (r *Root) writeFile(name string, data []byte, mode fs.FileMode, exclusive bool) error {
	if name == "." || name == "" || !filepath.IsLocal(name) {
		return &StageError{Stage: "name", Err: fs.ErrInvalid}
	}
	release, err := r.pinDirs(filepath.Dir(name))
	if err != nil {
		return &StageError{Stage: "create", Err: err}
	}
	defer release()
	return writeFile(filepath.Join(r.dir, filepath.Dir(name)), filepath.Base(name), data, mode, exclusive, defaultOperations())
}

func (r *Root) removeDurable(name string) error {
	release, err := r.pinDirs(filepath.Dir(name))
	if err != nil {
		return &StageError{Stage: "remove", Err: err}
	}
	defer release()
	return removeDurable(filepath.Join(r.dir, name))
}

func (r *Root) pinDir(name string) (windows.Handle, error) {
	path := filepath.Join(r.dir, name)
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(p,
		windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0)
	if err != nil {
		return 0, fmt.Errorf("pin %s: %w", path, err)
	}
	var pinned windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &pinned); err != nil {
		_ = windows.CloseHandle(h)
		return 0, fmt.Errorf("pin %s: %w", path, err)
	}
	if pinned.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || pinned.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(h)
		return 0, fmt.Errorf("pin %s: not a plain directory", path)
	}
	inRoot, err := r.r.Open(name)
	if err != nil {
		_ = windows.CloseHandle(h)
		return 0, fmt.Errorf("pin %s: %w", path, err)
	}
	var resolved windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(windows.Handle(inRoot.Fd()), &resolved)
	_ = inRoot.Close()
	if err != nil {
		_ = windows.CloseHandle(h)
		return 0, fmt.Errorf("pin %s: %w", path, err)
	}
	if pinned.VolumeSerialNumber != resolved.VolumeSerialNumber ||
		pinned.FileIndexHigh != resolved.FileIndexHigh || pinned.FileIndexLow != resolved.FileIndexLow {
		_ = windows.CloseHandle(h)
		return 0, fmt.Errorf("pin %s: not the directory the root resolves there", path)
	}
	return h, nil
}

func (r *Root) syncDirExported(string) error { return ErrDirectorySyncUnsupported }
