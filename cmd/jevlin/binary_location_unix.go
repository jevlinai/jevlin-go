//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// writableByOthers says why a user other than this one (or root) could
// replace fi's entries or contents, or "" when none can. The sticky bit is
// no exception: it protects a name only while its owner's file holds it.
func writableByOthers(fi os.FileInfo) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "has no ownership information"
	}
	uid := uint32(os.Getuid()) // #nosec G115 -- a uid is never negative on POSIX
	if st.Uid != uid && st.Uid != 0 {
		return fmt.Sprintf("is owned by another user (uid %d)", st.Uid)
	}
	mode := fi.Mode().Perm()
	if mode&0o002 != 0 {
		return "is writable by every user"
	}
	if mode&0o020 != 0 && !privateGroup(st.Gid) {
		return fmt.Sprintf("is writable by its group (gid %d)", st.Gid)
	}
	return ""
}

// privateGroup: is gid this user's own group — the primary group named after
// the user, as user-private-group systems create with umask 002 — so that
// group-write grants no one else anything?
func privateGroup(gid uint32) bool {
	if int64(gid) != int64(os.Getgid()) {
		return false
	}
	u, err := user.Current()
	if err != nil {
		return false
	}
	g, err := user.LookupGroupId(strconv.FormatUint(uint64(gid), 10))
	return err == nil && g.Name == u.Username
}
