// Package winacl is the Windows access-list handling that pkg/auth and
// cmd/jevlin both need: reading an object's owner and DACL into plain values,
// naming a SID, and giving an object a protected owner-only list.
//
// It exists because the two cannot share a file any other way. pkg/ must not
// import cmd/jevlin (AGENTS.md invariant 8), and the wrapper's wallet check and
// the shared store's state directory need the same three operations; a copy in
// each is a second parser of the same descriptor string, which is the kind of
// thing that drifts. This package imports nothing of the module, so neither
// side can reach the other through it.
//
// What is here without a build tag is the part that needs no Windows API — the
// values and the parser of a descriptor's string form — so it is tested on
// every OS. Reading, naming and setting are winacl_windows.go.
//
// Nothing in this package judges an access list. Whether an entry is a problem
// is the caller's question: the wallet counts who can read it, the state
// directory's doctor check counts who else can open it.
package winacl

import (
	"errors"
	"fmt"
	"strings"
)

// ErrReparsePoint is what Read reports for a name that is a symlink, a
// junction or any other reparse point: it is never read through, because what
// a reparse point leads to is not the object the caller named, and in a
// directory a sandboxed command can write it can lead anywhere.
var ErrReparsePoint = errors.New("is a link or other reparse point")

// The two principals every default Windows access list names besides the
// user, and the two that can take ownership of any object whatever its list
// says.
const (
	SIDLocalSystem    = "S-1-5-18"
	SIDAdministrators = "S-1-5-32-544"
)

// ACE is one access-list entry, reduced to what a judgment needs.
type ACE struct {
	// Allow is false for a deny entry and for any type that is not one of the
	// four allow types.
	Allow bool
	// Flags is the entry's header flags (inheritance, inherit-only, inherited).
	Flags uint8
	// Mask is the entry's access mask.
	Mask uint32
	// SID is the trustee, in S-1-… form.
	SID string
}

// Descriptor is what an object's security descriptor says about who controls it.
type Descriptor struct {
	// Owner is the owner's SID, empty if the descriptor names none.
	Owner string
	// Protected: the list does not inherit from the parent.
	Protected bool
	// NullDACL: the object has no access list at all, which grants everyone
	// everything. ACEs is empty then.
	NullDACL bool
	// ACEs are the entries, in the order the list holds them.
	ACEs []ACE
}

// IsAllowACEType reports the four allow entry types a DACL can hold (plain,
// object, callback, callback object). Each keeps its mask right after the
// header, which is why the same read serves them.
func IsAllowACEType(t uint8) bool {
	switch t {
	case 0x0, 0x5, 0x9, 0xB:
		return true
	}
	return false
}

// SDDLTrustees returns the trustee field of each ACE in a descriptor string's
// DACL, in order: "D:PAI(A;;FA;;;LA)(A;OICIIO;GA;;;LA)" -> [LA LA]. A
// conditional entry's expression carries parentheses of its own, so an entry
// ends at the parenthesis that closes it, not at the first one.
//
// The string form is how a trustee is named without reading the binary ACE's
// SID by pointer arithmetic, which would need unsafe; the module admits that in
// one file only. The caller pairs each trustee with the same-index binary entry
// and checks the two counts agree.
func SDDLTrustees(sddl string) ([]string, error) {
	start := strings.Index(sddl, "D:")
	if start < 0 {
		return nil, errors.New("the descriptor has no DACL")
	}
	var trustees []string
	depth, open := 0, -1
	for i := start + 2; i < len(sddl); i++ {
		switch sddl[i] {
		case '(':
			if depth == 0 {
				open = i
			}
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced access entry in %q", sddl)
			}
			if depth == 0 {
				fields := strings.SplitN(sddl[open+1:i], ";", 7)
				if len(fields) < 6 {
					return nil, fmt.Errorf("unexpected access entry %q", sddl[open:i+1])
				}
				trustees = append(trustees, fields[5])
			}
		default:
			// Past the DACL: a SACL section follows only when one was asked for.
			if depth == 0 && i+1 < len(sddl) && sddl[i+1] == ':' && open >= 0 {
				return trustees, nil
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unterminated access entry in %q", sddl)
	}
	return trustees, nil
}
