package fsx

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// moveDurable keeps the Windows durability barrier MoveFileDurable uses,
// MOVEFILE_WRITE_THROUGH, which os.Root's rename does not set. MoveFileEx
// takes paths, so each directory below the root that the move goes through
// is pinned first: opened by its path without FILE_SHARE_DELETE, which stops
// anyone renaming or deleting it while the handle is held, and checked to be
// a real directory (not a reparse point) that is the same directory the root
// itself resolves at that name. The path then names that directory for the
// whole move. The root's own directory is not pinned: replacing it needs
// write access to its parent, which is not a writable root.
func (r *Root) moveDurable(from, to string) error {
	var pins []windows.Handle
	defer func() {
		for _, h := range pins {
			_ = windows.CloseHandle(h)
		}
	}()
	for _, dir := range []string{filepath.Dir(to), filepath.Dir(from)} {
		if dir == "." {
			continue
		}
		h, err := r.pinDir(dir)
		if err != nil {
			return &StageError{Stage: "move", Err: err}
		}
		pins = append(pins, h)
	}
	if err := movePublication(filepath.Join(r.dir, from), filepath.Join(r.dir, to)); err != nil {
		return &StageError{Stage: "move", Err: err}
	}
	return nil
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
