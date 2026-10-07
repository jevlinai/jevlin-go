package auth

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Who may open the state directory and its files, where access is a DACL
// (Windows; perm_windows.go reads and sets it). A DACL is inherited, so a
// state_dir created outside jevlin setup — or one beneath a parent that later
// gains an inheritable entry — would otherwise silently admit every principal
// its parent does. OpenStore gives a directory it creates a protected
// owner-only DACL, and every open and every secret load re-verifies it here.
//
// SYSTEM and BUILTIN\Administrators are tolerated, as they are in the
// default profile DACL: both can take ownership of any object regardless.

const (
	sidLocalSystem    = "S-1-5-18"
	sidAdministrators = "S-1-5-32-544"
)

// stateACE is one access-list entry, reduced to what the judgment needs.
type stateACE struct {
	allow bool
	sid   string
}

// stateAccess is what one object's security descriptor says.
type stateAccess struct {
	owner string
	// nullDACL: the object has no access list, which grants everyone everything.
	nullDACL bool
	aces     []stateACE
}

// judgeStateAccess refuses an object owned by, or granting any access to, a
// principal other than user, SYSTEM and Administrators. Any allow entry
// counts, whatever its mask and inheritance flags: an inherit-only entry
// reaches every secret later created beneath the directory, and write access
// alone lets another principal replace a secret. Deny entries never widen
// access, so they play no part. name renders a SID for the refusal.
func judgeStateAccess(acc stateAccess, user string, name func(sid string) string) error {
	trusted := func(sid string) bool {
		return sid == user || sid == sidLocalSystem || sid == sidAdministrators
	}
	if acc.nullDACL {
		return errors.New("has no access list, so everyone can open it")
	}
	var problems []string
	if !trusted(acc.owner) {
		problems = append(problems, "is owned by "+name(acc.owner))
	}
	seen := map[string]bool{}
	var others []string
	for _, ace := range acc.aces {
		if !ace.allow || trusted(ace.sid) || seen[ace.sid] {
			continue
		}
		seen[ace.sid] = true
		others = append(others, name(ace.sid))
	}
	if len(others) > 0 {
		sort.Strings(others)
		problems = append(problems, "grants access to "+strings.Join(others, ", "))
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s (restrict it to your account)", strings.Join(problems, " and "))
}
