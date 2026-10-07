package fsx

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// noWait has no Windows meaning: a named pipe lives in its own namespace,
// never at a name in a directory, so a read there cannot block on one.
const noWait = 0

// openNoWait opens path for reading. A symlink or junction at the name is
// followed, which a read may do: a sandboxed command can already read what it
// could point the name at, and the regular-file check still holds.
func openNoWait(path string) (*os.File, error) {
	return os.Open(path) // #nosec G304 -- the caller's own directory; the result is checked to be a regular file before a byte is read
}

// openNoFollow opens path for reading, refusing a symlink or junction at the
// name: FILE_FLAG_OPEN_REPARSE_POINT opens it as itself, and the attribute
// check refuses it.
func openNoFollow(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		_ = windows.CloseHandle(h)
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(h)
		return nil, &fs.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	return os.NewFile(uintptr(h), path), nil
}

// openLock opens or creates path for locking with share mode 0, the Windows
// lock this client already uses: a second open fails with a sharing violation
// for as long as the handle is held. FILE_FLAG_OPEN_REPARSE_POINT opens a
// symlink or junction at the name as itself instead of following it, and the
// attribute check then refuses it.
func openLock(path string) (*os.File, error) {
	return openExclusive(path, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.OPEN_ALWAYS)
}

// openLockExisting is openLock read-only, on an existing file.
func openLockExisting(path string) (*os.File, error) {
	return openExclusive(path, windows.GENERIC_READ, windows.OPEN_EXISTING)
}

func openExclusive(path string, access, disposition uint32) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, access, 0, nil, disposition,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		_ = windows.CloseHandle(h)
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		_ = windows.CloseHandle(h)
		return nil, &fs.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	return os.NewFile(uintptr(h), path), nil
}
