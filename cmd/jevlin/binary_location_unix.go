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
// replace fi's entries or contents. refuse is a reason to stop; warn is one
// to report and go past; both are "" when no one else can. The sticky bit is
// no exception: it protects a name only while its owner's file holds it.
func writableByOthers(fi os.FileInfo) (refuse, warn string) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "has no ownership information", ""
	}
	uid := uint32(os.Getuid()) // #nosec G115 -- a uid is never negative on POSIX
	if st.Uid != uid && st.Uid != 0 {
		return fmt.Sprintf("is owned by another user (uid %d)", st.Uid), ""
	}
	mode := fi.Mode().Perm()
	if mode&0o002 != 0 {
		return "is writable by every user", ""
	}
	if mode&0o020 == 0 {
		return "", ""
	}
	name := groupName(st.Gid)
	if privateGroup(st.Gid, name) || rootEquivalentGroup(name) {
		return "", ""
	}
	if name == "" {
		return "", fmt.Sprintf("is writable by its group (gid %d)", st.Gid)
	}
	return "", fmt.Sprintf("is writable by its group %s (gid %d)", name, st.Gid)
}

// groupName names gid, or "" when it cannot be looked up. A variable so a
// test can give a directory the group a Homebrew prefix has without being
// able to chgrp to it.
var groupName = func(gid uint32) string {
	g, err := user.LookupGroupId(strconv.FormatUint(uint64(gid), 10))
	if err != nil {
		return ""
	}
	return g.Name
}

// privateGroup: is gid this user's own group — the primary group named after
// the user, as user-private-group systems create with umask 002 — so that
// group-write grants no one else anything?
func privateGroup(gid uint32, name string) bool {
	if int64(gid) != int64(os.Getgid()) || name == "" {
		return false
	}
	u, err := user.Current()
	return err == nil && name == u.Username
}

// rootEquivalentGroup: are this group's members already able to become root,
// so that its write bit hands them nothing they do not have? These are the
// groups macOS (admin, wheel) and the Linux distributions (sudo, wheel, and
// root's own) grant sudo or own system directories with by default. Homebrew
// makes its prefix group-writable by admin, so without this a binary npm put
// there — the README's first install path — would be refused. Judged by name,
// because the gids differ between systems; a machine that gives one of these
// names to an unprivileged group is trusted further than it should be, and
// the check is a guard against a mistake, not against the machine's owner.
func rootEquivalentGroup(name string) bool {
	switch name {
	case "root", "wheel", "admin", "sudo":
		return true
	}
	return false
}
