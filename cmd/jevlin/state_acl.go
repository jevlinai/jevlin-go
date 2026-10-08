package main

// Who can open the state directory, as doctor reports it.
//
// On Windows access is a DACL and a DACL is inherited. `jevlin setup` gives
// <home>\state a protected owner-only one, and OpenStore gives the same to a
// `[mining] state_dir` it creates (pkg/auth/perm.go). A directory that was made
// some other way, or that a program has since granted an entry, can admit more:
// a state_dir under D:\ inherits whatever D:\ grants, and a coding agent's
// sandbox setup grants its sandbox group an entry on exactly this directory,
// because the directory is one of the sandbox's writable roots.
//
// So another principal on the list is not an error and nothing refuses it: no
// open of the store and no load of a secret reads a DACL. This is the one place
// that does, and it reads and reports and changes nothing. What it treats as a
// problem is what no setup of this client produces and no participant would
// intend: a directory or credential with no access list at all (everyone), or
// owned by someone else (an owner keeps WRITE_DAC whatever the list says).
// Everyone else it finds, it names, for the participant to judge.
//
// The judgment, the walk and the verdict are here and the same on every
// platform, over winacl.Descriptor; the platform half — reading a DACL, naming
// a SID — is state_acl_windows.go and state_acl_other.go. Naming a SID asks the
// system to look the account up, which can reach a domain controller, so it is
// done here, for the report, and never by the store on a search's path.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jevlinai/jevlin-go/internal/winacl"
)

// stateACLBackend is the platform half. managed is false where file modes are
// the whole story, and then nothing else in it is called.
type stateACLBackend struct {
	managed bool
	// read returns one object's owner and access list. It is only called on an
	// object isLink has cleared, because reading a DACL by name follows a link.
	read func(path string) (winacl.Descriptor, error)
	// user is the SID of the account running jevlin.
	user func() (string, error)
	// name renders a SID for a person: DOMAIN\name where it resolves.
	name func(sid string) string
	// isLink reports a symlink or any other reparse point.
	isLink func(info fs.FileInfo) bool
}

// stateACL is the running platform's backend; tests substitute a fake.
var stateACL = systemStateACL()

// stateCheckedHook runs between inspect's checks on a name and the read of its
// access list, when a test sets it: the seam a test uses to replace the object
// at exactly that moment, as secretCheckedHook does for readSecret. Nil in
// production.
var stateCheckedHook func(path string)

// stateNotRead is what is said of an object whose access list was not read
// because it is a link, or not the kind of object it should be.
func stateNotRead(path string) error {
	return fmt.Errorf("%s is a link or not the kind of object it should be, so its access list was not read", path)
}

// stateCreatorPlaceholders are the SIDs that stand for "whoever creates an
// object here" in an access list a directory hands down. They name nobody: an
// inheritable CREATOR OWNER entry on a parent reaches a child as an effective
// entry for the actual creator and an inherit-only copy of the placeholder, and
// the copy grants nothing to anyone.
var stateCreatorPlaceholders = map[string]bool{
	"S-1-3-0": true, // CREATOR OWNER
	"S-1-3-1": true, // CREATOR GROUP
}

// stateFindings is what one object's descriptor says.
type stateFindings struct {
	// Problems are clauses that complete "<object> …": no access list, or an
	// owner who is not the user.
	Problems []string
	// Others names, sorted and each once, the principals other than the user,
	// SYSTEM and Administrators that the list gives an allow entry to.
	Others []string
}

// judgeStateDescriptor is the judgment, a pure function of the descriptor, the
// user's SID and a way to render a SID. SYSTEM and BUILTIN\Administrators are
// not counted either way: both can take ownership of any object whatever its
// list says, and the default profile list names them. An entry's mask and
// inheritance flags play no part — an inherit-only entry reaches every secret
// later created beneath the directory, and write access alone lets another
// principal replace a secret — except that an entry granting nothing is not an
// entry. A deny entry never widens access, so it is not one either.
func judgeStateDescriptor(d winacl.Descriptor, user string, name func(sid string) string) stateFindings {
	trusted := func(sid string) bool {
		return sid == user || sid == winacl.SIDLocalSystem || sid == winacl.SIDAdministrators
	}
	var f stateFindings
	if d.NullDACL {
		f.Problems = append(f.Problems, "has no access list, so everyone can open it")
	}
	switch {
	case d.Owner == "":
		f.Problems = append(f.Problems, "has no owner on record")
	case !trusted(d.Owner):
		f.Problems = append(f.Problems, "is owned by "+name(d.Owner)+", who can change who may open it")
	}
	seen := map[string]bool{}
	for _, ace := range d.ACEs {
		if !ace.Allow || ace.Mask == 0 || trusted(ace.SID) || stateCreatorPlaceholders[ace.SID] || seen[ace.SID] {
			continue
		}
		seen[ace.SID] = true
		f.Others = append(f.Others, name(ace.SID))
	}
	sort.Strings(f.Others)
	return f
}

// stateObject is one inspected object that has something to say.
type stateObject struct {
	Path     string
	Problems []string
	Others   []string
}

// stateAccessFacts is doctor's view of the installation's state directory.
type stateAccessFacts struct {
	// Checked is false where access lists are not managed; doctor then has no
	// state access check at all.
	Checked bool
	Dir     string
	Present bool
	// Objects are the directory and the credential files in it that have a
	// problem or admit someone else; one that says nothing is not listed.
	Objects []stateObject
	// Err joins every object that could not be read.
	Err error
}

// inspectStateAccess reads, and only reads, the access list of dir and of each
// of the named files in it that exists. It lists nothing: the directory is one
// a sandboxed command can fill, so the files are asked for by name.
func inspectStateAccess(dir string, files []string) stateAccessFacts {
	f := stateAccessFacts{Checked: stateACL.managed, Dir: dir}
	if !f.Checked {
		return f
	}
	if dir == "" {
		f.Err = errors.New("no state directory is configured")
		return f
	}
	user, err := stateACL.user()
	if err != nil {
		f.Err = err
		return f
	}
	var problems []error
	// inspect reads one object. present is whether anything is at the name;
	// read is whether its access list was read, which a link, an object of the
	// wrong kind and a failed read all prevent.
	inspect := func(path string, wantDir bool) (present, read bool) {
		info, err := os.Lstat(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return false, false
		case err != nil:
			problems = append(problems, err)
			return true, false
		case stateACL.isLink(info) || info.IsDir() != wantDir || (!wantDir && !info.Mode().IsRegular()):
			problems = append(problems, stateNotRead(path))
			return true, false
		}
		if stateCheckedHook != nil {
			stateCheckedHook(path)
		}
		// The check above only classifies early. The name is in a directory a
		// sandboxed command can write, so it can be replaced between that check
		// and this read; the read therefore opens the object once, refuses a
		// reparse point on the handle itself, and reads from the handle
		// (winacl.Read), and that refusal is what keeps a link from being read.
		d, err := stateACL.read(path)
		if errors.Is(err, winacl.ErrReparsePoint) {
			problems = append(problems, stateNotRead(path))
			return true, false
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", path, err))
			return true, false
		}
		if found := judgeStateDescriptor(d, user, stateACL.name); len(found.Problems) > 0 || len(found.Others) > 0 {
			f.Objects = append(f.Objects, stateObject{Path: path, Problems: found.Problems, Others: found.Others})
		}
		return true, true
	}
	var dirRead bool
	f.Present, dirRead = inspect(dir, true)
	// The files are looked at only through a directory that was itself read:
	// a name inside a link would be the link's target's.
	if dirRead {
		for _, name := range files {
			inspect(filepath.Join(dir, name), false)
		}
	}
	f.Err = errors.Join(problems...)
	return f
}

// doctorStateCheck is the verdict. NO is a fact the participant did not intend
// and can act on; another principal on the list is information, so it is OK
// with the names in the detail; UNKNOWN is the absence of a fact. A problem
// outranks an object that could not be read, since it is a fact whatever else
// could not be.
func doctorStateCheck(f stateAccessFacts) doctorCheck {
	c := doctorCheck{Name: "state access"}
	var problems, targets []string
	for _, o := range f.Objects {
		if len(o.Problems) > 0 {
			problems = append(problems, o.Path+" "+strings.Join(o.Problems, " and "))
			targets = append(targets, o.Path)
		}
	}
	others := stateOthersDetail(f.Objects)
	switch {
	case len(problems) > 0:
		c.Verdict = verdictNo
		c.Detail = strings.Join(problems, "; ")
		if others != "" {
			c.Detail += "; " + others
		}
		if f.Err != nil {
			c.Detail += " (and some of it could not be checked: " + singleLineError(f.Err) + ")"
		}
		c.Fix = stateFix(f.Dir, targets)
	case f.Err != nil:
		c.Verdict = verdictUnknown
		c.Detail = "could not read who can open the state directory " + f.Dir + " — " + singleLineError(f.Err)
		if others != "" {
			c.Detail += "; " + others
		}
	case !f.Present:
		c.Verdict = verdictOK
		c.Detail = "no state directory in " + f.Dir + " yet"
	case others != "":
		c.Verdict = verdictOK
		c.Detail = others + "; an agent's sandbox is meant to reach this directory, so an entry for one is expected — the store and doctor leave it, " +
			"but jevlin setup resets <home>\\state to an owner-only list on every run, which removes it — " +
			"if you do not recognize who is named, give " + f.Dir + " an access list that only you hold"
	default:
		c.Verdict = verdictOK
		c.Detail = "only you can open " + f.Dir + " and the credentials in it"
	}
	return c
}

// stateFix says what to do about the objects that have a problem, by name: the
// directory and a credential in it are separate objects with separate owners and
// lists, and fixing the directory does not reach a file that has its own. A
// directory's fix says so, and says how to carry the new owner and entries down.
func stateFix(dir string, targets []string) string {
	fix := "give " + strings.Join(targets, ", ") + " an access list that only you hold: in each one's Properties, " +
		"Security, Advanced, make yourself the owner, turn off inheritance and remove every other entry"
	for _, t := range targets {
		if t == dir {
			return fix + "; on " + dir + " also tick \"Replace owner on subcontainers and objects\" and \"Replace all child object " +
				"permission entries with inheritable permission entries from this object\", so the credentials inside it follow"
		}
	}
	return fix
}

// stateOthersDetail says who else can open what, one clause per distinct set of
// principals, in the order the objects were inspected: a directory and the
// files that inherit its list are one clause, not five.
func stateOthersDetail(objects []stateObject) string {
	var order []string
	paths := map[string][]string{}
	for _, o := range objects {
		if len(o.Others) == 0 {
			continue
		}
		key := strings.Join(o.Others, ", ")
		if _, seen := paths[key]; !seen {
			order = append(order, key)
		}
		paths[key] = append(paths[key], o.Path)
	}
	clauses := make([]string, 0, len(order))
	for _, key := range order {
		clauses = append(clauses, "the access list on "+strings.Join(paths[key], ", ")+" also names "+key)
	}
	return strings.Join(clauses, "; ")
}
