package fsx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// maxLinkHops bounds the walk ReplaceFile takes through symbolic links, as
// the kernel bounds path resolution, so a loop of links is an error rather
// than a hang.
const maxLinkHops = 40

// ReplaceFile replaces the file at path with complete data, as
// WriteFileAtomic does: a temporary file in the same directory, synced, then
// renamed over the name, so a reader sees the old bytes or the new ones and
// never a mix, whenever the writer stops.
//
// It is for a file somebody else owns and arranges, such as a coding agent's
// settings, and differs from WriteFileAtomic in two ways:
//
//   - A symbolic link at path is followed to the file it names, and that file
//     is replaced: the link stays a link. A settings file kept in a dotfiles
//     repository, with a link to it from where the agent looks, keeps that
//     arrangement. The temporary file is created beside the target, in the
//     target's directory, so the rename stays within one directory.
//   - An existing file keeps its permission bits; mode is used only for a file
//     that does not exist yet.
//   - A file this process could not write in place is not replaced. A rename
//     needs only the directory to be writable, so without the check a file
//     the participant made read-only, or one another account owns, would be
//     replaced where an in-place write was refused. It is opened for writing,
//     without truncating, and closed; if that fails, so does ReplaceFile,
//     with that error.
//
// What a rename cannot keep, it does not: the replaced file is a new inode,
// so a hard link to the old one keeps the old bytes, and a watcher holding
// the old inode sees it removed rather than written.
//
// The temporary file's name is ".tmp-" and random digits, so a copy left by a
// writer that was killed carries no suffix a host loads by pattern (.json,
// .yaml, .toml).
func ReplaceFile(path string, data []byte, mode fs.FileMode) error {
	return replaceFile(path, data, mode, defaultOperations())
}

func replaceFile(path string, data []byte, mode fs.FileMode, ops operations) error {
	target, err := linkTarget(path)
	if err != nil {
		return &StageError{Stage: "resolve", Err: err}
	}
	info, err := os.Lstat(target)
	switch {
	case err == nil && !info.Mode().IsRegular():
		return &StageError{Stage: "resolve", Err: fmt.Errorf("%s is not a regular file", target)}
	case err == nil:
		mode = info.Mode().Perm()
		probe, err := os.OpenFile(target, os.O_WRONLY, 0) // #nosec G304 -- the caller's own file, opened only to ask whether it may be written
		if err != nil {
			return &StageError{Stage: "open", Err: err}
		}
		_ = probe.Close()
	case !errors.Is(err, fs.ErrNotExist):
		return &StageError{Stage: "resolve", Err: err}
	}
	return writeFile(filepath.Dir(target), filepath.Base(target), data, mode, false, ops)
}

// linkTarget follows symbolic links from path to the name that is not one:
// an existing file, or the name a dangling link points at, which is where a
// write through the link would create the file.
//
// A relative link is read from the real directory the link sits in, as the
// kernel reads it, not from the text of its path: when a directory on the
// way is itself a link, "dir/../x" named lexically is a different file from
// the one the kernel opens, so the link's directory is resolved first, at
// every hop.
func linkTarget(path string) (string, error) {
	for hop := 0; hop < maxLinkHops; hop++ {
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return path, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			return path, nil
		}
		dest, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(dest) {
			dir, err := filepath.EvalSymlinks(filepath.Dir(path))
			if err != nil {
				return "", err
			}
			dest = filepath.Join(dir, dest)
		}
		path = dest
	}
	return "", fmt.Errorf("%s: more than %d symbolic links", path, maxLinkHops)
}
