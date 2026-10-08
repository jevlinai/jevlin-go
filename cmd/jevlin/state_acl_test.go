package main

// The platform-independent half of state_acl.go, on every OS: the judgment of
// a descriptor, the verdict, what the gatherer reads, and where doctor wires
// it in. A fake backend stands in for the Windows DACL calls and records every
// path it is asked to read; state_acl_windows_test.go proves the same against
// what Windows actually holds.

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/internal/winacl"
	"github.com/jevlinai/jevlin-go/pkg/auth"
)

const (
	stateTestUser      = "S-1-5-21-1-2-3-1001"
	stateTestOther     = "S-1-5-21-1-2-3-1002"
	stateTestUsers     = "S-1-5-32-545"
	stateTestLogon     = "S-1-5-5-0-4242"
	stateTestEveryone  = "S-1-1-0"
	stateFullControl   = 0x001F01FF
	stateGenericAll    = 0x10000000
	stateReadExecute   = 0x001200A9
	aceInherited       = 0x10
	aceInheritOnlyCIOI = 0x08 | 0x03
)

var stateTestNames = map[string]string{
	stateTestUsers:    `BUILTIN\Users`,
	stateTestLogon:    `NT AUTHORITY\LogonSessionId_0_4242`,
	stateTestOther:    `HOST\Bob`,
	stateTestEveryone: `Everyone`,
}

func stateTestName(sid string) string {
	if n, ok := stateTestNames[sid]; ok {
		return n
	}
	return sid
}

func allowACE(sid string, flags uint8, mask uint32) winacl.ACE {
	return winacl.ACE{Allow: true, Flags: flags, Mask: mask, SID: sid}
}

// fakeStateACL is a managed backend over a map. An object it has no entry for
// is what setup leaves: owned by the user, with one allow entry for the user.
type fakeStateACL struct {
	desc   map[string]winacl.Descriptor
	failOn map[string]error
	read   []string // every path asked for, in order
}

func useFakeStateACL(t *testing.T) *fakeStateACL {
	t.Helper()
	f := &fakeStateACL{desc: map[string]winacl.Descriptor{}, failOn: map[string]error{}}
	saved := stateACL
	stateACL = stateACLBackend{
		managed: true,
		read:    f.readDescriptor,
		user:    func() (string, error) { return stateTestUser, nil },
		name:    stateTestName,
		isLink:  saved.isLink,
	}
	t.Cleanup(func() { stateACL = saved })
	return f
}

func (f *fakeStateACL) readDescriptor(path string) (winacl.Descriptor, error) {
	f.read = append(f.read, path)
	if err := f.failOn[path]; err != nil {
		return winacl.Descriptor{}, err
	}
	if d, ok := f.desc[path]; ok {
		return d, nil
	}
	return winacl.Descriptor{Owner: stateTestUser, Protected: true, ACEs: []winacl.ACE{allowACE(stateTestUser, 0, stateFullControl)}}, nil
}

// The judgment is a pure function of the descriptor, so every case a Windows
// list can produce is a row here rather than a fixture on a runner.
func TestStateDescriptorJudgment(t *testing.T) {
	cases := []struct {
		name         string
		d            winacl.Descriptor
		wantProblems []string // substrings, one per problem
		wantOthers   []string
	}{
		{"owner only, as setup leaves it",
			winacl.Descriptor{Owner: stateTestUser, ACEs: []winacl.ACE{allowACE(stateTestUser, 0, stateFullControl), allowACE(stateTestUser, 0x0B, stateGenericAll)}}, nil, nil},
		{"the default profile list: SYSTEM, Administrators and the user",
			winacl.Descriptor{Owner: stateTestUser, ACEs: []winacl.ACE{
				allowACE(winacl.SIDLocalSystem, 0, stateFullControl), allowACE(winacl.SIDAdministrators, 0, stateFullControl), allowACE(stateTestUser, 0, stateFullControl)}}, nil, nil},
		{"an elevated process's default owner",
			winacl.Descriptor{Owner: winacl.SIDAdministrators, ACEs: []winacl.ACE{allowACE(stateTestUser, 0, stateFullControl)}}, nil, nil},
		{"SYSTEM as owner",
			winacl.Descriptor{Owner: winacl.SIDLocalSystem, ACEs: []winacl.ACE{allowACE(stateTestUser, 0, stateFullControl)}}, nil, nil},
		{"an inherited entry for Users",
			winacl.Descriptor{Owner: stateTestUser, ACEs: []winacl.ACE{allowACE(stateTestUser, 0, stateFullControl), allowACE(stateTestUsers, aceInherited, stateReadExecute)}},
			nil, []string{`BUILTIN\Users`}},
		{"an inherit-only entry reaches what is created later",
			winacl.Descriptor{Owner: stateTestUser, ACEs: []winacl.ACE{allowACE(stateTestLogon, aceInheritOnlyCIOI, stateGenericAll)}},
			nil, []string{`NT AUTHORITY\LogonSessionId_0_4242`}},
		{"a deny entry is not another principal with access",
			winacl.Descriptor{Owner: stateTestUser, ACEs: []winacl.ACE{{Allow: false, Mask: stateReadExecute, SID: stateTestUsers}, allowACE(stateTestUser, 0, stateFullControl)}}, nil, nil},
		{"an allow entry that grants nothing is not one",
			winacl.Descriptor{Owner: stateTestUser, ACEs: []winacl.ACE{allowACE(stateTestUsers, 0, 0)}}, nil, nil},
		{"CREATOR OWNER and CREATOR GROUP name nobody",
			winacl.Descriptor{Owner: stateTestUser, ACEs: []winacl.ACE{allowACE("S-1-3-0", aceInheritOnlyCIOI, stateGenericAll), allowACE("S-1-3-1", aceInheritOnlyCIOI, stateGenericAll)}}, nil, nil},
		{"each principal once, sorted by the name shown",
			winacl.Descriptor{Owner: stateTestUser, ACEs: []winacl.ACE{
				allowACE(stateTestLogon, 0, stateReadExecute), allowACE(stateTestUsers, 0, stateReadExecute), allowACE(stateTestLogon, aceInherited, stateReadExecute)}},
			nil, []string{`BUILTIN\Users`, `NT AUTHORITY\LogonSessionId_0_4242`}},
		{"another user as owner",
			winacl.Descriptor{Owner: stateTestOther, ACEs: []winacl.ACE{allowACE(stateTestUser, 0, stateFullControl)}},
			[]string{`is owned by HOST\Bob`}, nil},
		{"no access list at all",
			winacl.Descriptor{Owner: stateTestUser, NullDACL: true},
			[]string{"has no access list, so everyone can open it"}, nil},
		{"no access list and another owner are both said",
			winacl.Descriptor{Owner: stateTestOther, NullDACL: true},
			[]string{"has no access list", `is owned by HOST\Bob`}, nil},
		{"no owner on record",
			winacl.Descriptor{ACEs: []winacl.ACE{allowACE(stateTestUser, 0, stateFullControl)}},
			[]string{"no owner"}, nil},
		{"a foreign owner and another reader are separate findings",
			winacl.Descriptor{Owner: stateTestOther, ACEs: []winacl.ACE{allowACE(stateTestUser, 0, stateFullControl), allowACE(stateTestUsers, aceInherited, stateReadExecute)}},
			[]string{`is owned by HOST\Bob`}, []string{`BUILTIN\Users`}},
	}
	for _, c := range cases {
		got := judgeStateDescriptor(c.d, stateTestUser, stateTestName)
		if len(got.Problems) != len(c.wantProblems) {
			t.Errorf("%s: problems = %q, want %d matching %q", c.name, got.Problems, len(c.wantProblems), c.wantProblems)
		} else {
			for i, want := range c.wantProblems {
				if !strings.Contains(got.Problems[i], want) {
					t.Errorf("%s: problem %d = %q, want it to contain %q", c.name, i, got.Problems[i], want)
				}
			}
		}
		if !reflect.DeepEqual(got.Others, c.wantOthers) {
			t.Errorf("%s: others = %q, want %q", c.name, got.Others, c.wantOthers)
		}
	}
}

// Naming a SID asks the system to look the account up. The judgment does it
// for the principals it reports and for no others, so the common case — a list
// that names only the user, SYSTEM and Administrators — resolves nothing.
func TestStateDescriptorJudgmentResolvesOnlyWhatItReports(t *testing.T) {
	var asked []string
	name := func(sid string) string { asked = append(asked, sid); return sid }
	clean := winacl.Descriptor{Owner: winacl.SIDAdministrators, ACEs: []winacl.ACE{
		allowACE(winacl.SIDLocalSystem, 0, stateFullControl), allowACE(winacl.SIDAdministrators, 0, stateFullControl), allowACE(stateTestUser, 0, stateFullControl),
		{Allow: false, Mask: stateReadExecute, SID: stateTestUsers}, allowACE("S-1-3-0", aceInheritOnlyCIOI, stateGenericAll)}}
	judgeStateDescriptor(clean, stateTestUser, name)
	if len(asked) != 0 {
		t.Errorf("a list naming nobody to report resolved %v", asked)
	}
	judgeStateDescriptor(winacl.Descriptor{Owner: stateTestOther, ACEs: []winacl.ACE{allowACE(stateTestUsers, 0, stateReadExecute), allowACE(stateTestUsers, aceInherited, stateReadExecute)}}, stateTestUser, name)
	sort.Strings(asked)
	if want := []string{stateTestOther, stateTestUsers}; !reflect.DeepEqual(asked, want) {
		t.Errorf("resolved %v, want the owner and Users once each", asked)
	}
}

// doctor's verdicts, from the facts.
func TestDoctorStateAccessCheck(t *testing.T) {
	dir := filepath.Join("home", "state")
	key := filepath.Join(dir, "dpop.key")
	agent := filepath.Join(dir, "agent.json")
	cases := []struct {
		name        string
		facts       stateAccessFacts
		verdict     doctorVerdict
		detail, fix []string
		notDetail   []string
	}{
		{"no state directory configured",
			stateAccessFacts{Checked: true, Err: errors.New("no state directory is configured")},
			verdictUnknown, []string{"no state directory is configured"}, nil, nil},
		{"no state directory yet", stateAccessFacts{Checked: true, Dir: dir},
			verdictOK, []string{"no state directory in " + dir + " yet"}, nil, nil},
		{"only you", stateAccessFacts{Checked: true, Dir: dir, Present: true},
			verdictOK, []string{"only you can open " + dir}, nil, []string{"also names"}},
		{"another principal, on the directory and what inherits it: one clause",
			stateAccessFacts{Checked: true, Dir: dir, Present: true, Objects: []stateObject{
				{Path: dir, Others: []string{`BUILTIN\Users`}}, {Path: key, Others: []string{`BUILTIN\Users`}}, {Path: agent, Others: []string{`BUILTIN\Users`}}}},
			verdictOK, []string{"the access list on " + dir + ", " + key + ", " + agent + " also names BUILTIN\\Users", "is meant to reach this directory", "do not recognize"}, nil, []string{"only you can open"}},
		{"two different sets are two clauses",
			stateAccessFacts{Checked: true, Dir: dir, Present: true, Objects: []stateObject{
				{Path: dir, Others: []string{`BUILTIN\Users`}}, {Path: key, Others: []string{`BUILTIN\Users`, `HOST\Bob`}}}},
			verdictOK, []string{"the access list on " + dir + " also names BUILTIN\\Users; the access list on " + key + " also names BUILTIN\\Users, HOST\\Bob"}, nil, nil},
		{"a credential with no access list",
			stateAccessFacts{Checked: true, Dir: dir, Present: true, Objects: []stateObject{{Path: key, Problems: []string{"has no access list, so everyone can open it"}}}},
			verdictNo, []string{key + " has no access list, so everyone can open it"}, []string{"access list that only you hold", "make yourself the owner"}, nil},
		{"a directory owned by someone else",
			stateAccessFacts{Checked: true, Dir: dir, Present: true, Objects: []stateObject{{Path: dir, Problems: []string{`is owned by HOST\Bob, who can change who may open it`}}}},
			verdictNo, []string{dir + ` is owned by HOST\Bob`}, []string{dir}, nil},
		{"a problem keeps what else was found",
			stateAccessFacts{Checked: true, Dir: dir, Present: true, Objects: []stateObject{
				{Path: dir, Others: []string{`BUILTIN\Users`}}, {Path: key, Problems: []string{"has no access list, so everyone can open it"}}}},
			verdictNo, []string{key + " has no access list", "the access list on " + dir + " also names BUILTIN\\Users"}, []string{"access list that only you hold"}, nil},
		{"unreadable",
			stateAccessFacts{Checked: true, Dir: dir, Present: true, Err: errors.Join(errors.New("first"), errors.New("second"))},
			verdictUnknown, []string{"could not read who can open the state directory " + dir, "first; second"}, nil, nil},
		{"unreadable, with another principal found",
			stateAccessFacts{Checked: true, Dir: dir, Present: true, Err: errors.New("x"), Objects: []stateObject{{Path: dir, Others: []string{`BUILTIN\Users`}}}},
			verdictUnknown, []string{"x", "also names BUILTIN\\Users"}, nil, nil},
		{"a problem outranks what could not be read",
			stateAccessFacts{Checked: true, Dir: dir, Present: true, Err: errors.New("x"), Objects: []stateObject{{Path: key, Problems: []string{"has no access list, so everyone can open it"}}}},
			verdictNo, []string{key, "could not be checked: x"}, []string{"access list that only you hold"}, nil},
	}
	for _, c := range cases {
		got := doctorStateCheck(c.facts)
		if got.Name != "state access" || got.Verdict != c.verdict {
			t.Errorf("%s: %s %s (%s)", c.name, got.Name, got.Verdict, got.Detail)
		}
		for _, want := range c.detail {
			if !strings.Contains(got.Detail, want) {
				t.Errorf("%s: detail %q missing %q", c.name, got.Detail, want)
			}
		}
		for _, not := range c.notDetail {
			if strings.Contains(got.Detail, not) {
				t.Errorf("%s: detail %q contains %q", c.name, got.Detail, not)
			}
		}
		if strings.Contains(got.Detail, "\n") {
			t.Errorf("%s: detail spans lines: %q", c.name, got.Detail)
		}
		if c.verdict != verdictNo && got.Fix != "" {
			t.Errorf("%s: offers a fix %q for a verdict that is not NO", c.name, got.Fix)
		}
		if c.verdict == verdictNo && got.Fix == "" {
			t.Errorf("%s: NO with nothing to do about it", c.name)
		}
		for _, want := range c.fix {
			if !strings.Contains(got.Fix, want) {
				t.Errorf("%s: fix %q missing %q", c.name, got.Fix, want)
			}
		}
	}
}

// The gatherer reads the directory and the credentials it is told to ask for,
// by name, and nothing else: not a stray file, not a subdirectory, not a name
// that is a link. And it changes nothing.
func TestInspectStateAccessAsksForNamedObjectsOnly(t *testing.T) {
	fake := useFakeStateACL(t)
	dir := filepath.Join(t.TempDir(), "state")
	key, agent := filepath.Join(dir, "dpop.key"), filepath.Join(dir, "agent.json")
	for _, p := range []string{key, agent, filepath.Join(dir, "stray.json"), filepath.Join(dir, "spool", "a.json")} {
		writeFileT(t, p, "x")
	}
	fake.desc[dir] = winacl.Descriptor{Owner: stateTestUser, ACEs: []winacl.ACE{allowACE(stateTestUser, 0, stateFullControl), allowACE(stateTestUsers, 0, stateReadExecute)}}
	fake.desc[key] = winacl.Descriptor{Owner: stateTestUser, NullDACL: true}
	before := listNames(t, dir)

	f := inspectStateAccess(dir, auth.CredentialFiles())
	if !f.Checked || !f.Present || f.Err != nil {
		t.Fatalf("%+v", f)
	}
	if want := []string{dir, key, agent}; !reflect.DeepEqual(fake.read, want) {
		t.Errorf("read %v, want exactly %v: the directory, then each credential that exists, in the list's order", fake.read, want)
	}
	if len(f.Objects) != 2 || f.Objects[0].Path != dir || !reflect.DeepEqual(f.Objects[0].Others, []string{`BUILTIN\Users`}) ||
		f.Objects[1].Path != key || len(f.Objects[1].Problems) != 1 {
		t.Errorf("objects = %+v, want the directory with Users and the key with a problem, and nothing for agent.json", f.Objects)
	}
	if after := listNames(t, dir); !reflect.DeepEqual(before, after) {
		t.Errorf("inspecting changed the directory: %v -> %v", before, after)
	}

	if absent := inspectStateAccess(filepath.Join(t.TempDir(), "none"), auth.CredentialFiles()); absent.Present || absent.Err != nil || !absent.Checked {
		t.Errorf("an absent directory: %+v", absent)
	}
	if unconfigured := inspectStateAccess("", auth.CredentialFiles()); unconfigured.Err == nil {
		t.Errorf("no directory configured: %+v", unconfigured)
	}
	fake.failOn[agent] = errors.New("injected: access denied")
	if failed := inspectStateAccess(dir, auth.CredentialFiles()); failed.Err == nil || !strings.Contains(failed.Err.Error(), "injected: access denied") || len(failed.Objects) != 2 {
		t.Errorf("a credential that cannot be read: %+v", failed)
	}
}

// A link is never read through: reading a DACL by name follows it, and a name
// inside a link directory is the target's. This is the state directory, which
// a sandboxed command can write.
func TestInspectStateAccessDoesNotFollowALink(t *testing.T) {
	fake := useFakeStateACL(t)
	root := t.TempDir()
	real := filepath.Join(root, "real")
	writeFileT(t, filepath.Join(real, "dpop.key"), "x")
	writeFileT(t, filepath.Join(root, "outside.json"), "x")

	t.Run("a credential that is a link", func(t *testing.T) {
		dir := filepath.Join(root, "state")
		writeFileT(t, filepath.Join(dir, "dpop.key"), "x")
		link := filepath.Join(dir, "agent.json")
		if err := os.Symlink(filepath.Join(root, "outside.json"), link); err != nil {
			t.Skipf("symlinks unavailable here: %v", err)
		}
		fake.read = nil
		f := inspectStateAccess(dir, auth.CredentialFiles())
		if f.Err == nil || !strings.Contains(f.Err.Error(), link) || !strings.Contains(f.Err.Error(), "link") {
			t.Errorf("err = %v, want it to name the link", f.Err)
		}
		for _, p := range fake.read {
			if p == link || strings.HasPrefix(p, root+string(filepath.Separator)+"outside") {
				t.Errorf("read %s, which is the link or its target", p)
			}
		}
	})
	t.Run("the directory itself is a link", func(t *testing.T) {
		dir := filepath.Join(root, "linked-state")
		if err := os.Symlink(real, dir); err != nil {
			t.Skipf("symlinks unavailable here: %v", err)
		}
		fake.read = nil
		f := inspectStateAccess(dir, auth.CredentialFiles())
		if f.Err == nil || !strings.Contains(f.Err.Error(), "link") {
			t.Errorf("err = %v, want a link named", f.Err)
		}
		if len(fake.read) != 0 {
			t.Errorf("read %v through a directory that is a link", fake.read)
		}
	})
}

// The check on a name only classifies early. A name in a directory a sandboxed
// command can write can be replaced between that check and the read, so the
// read itself refuses a reparse point, on its handle (winacl.Read), and doctor
// says of it what it says of a link it saw first: not read, and no list
// reported under the credential's name.
func TestInspectStateAccessRefusesWhatTheReadFindsIsALink(t *testing.T) {
	fake := useFakeStateACL(t)
	dir := filepath.Join(t.TempDir(), "state")
	key := filepath.Join(dir, "dpop.key")
	writeFileT(t, key, "x")
	fake.failOn[key] = &fs.PathError{Op: "open", Path: key, Err: winacl.ErrReparsePoint}

	f := inspectStateAccess(dir, auth.CredentialFiles())
	if f.Err == nil || f.Err.Error() != stateNotRead(key).Error() {
		t.Errorf("err = %v, want %q", f.Err, stateNotRead(key))
	}
	for _, o := range f.Objects {
		if o.Path == key {
			t.Errorf("an access list was reported under %s: %+v", key, o)
		}
	}
	// Any other failure of the read is still its own sentence, with the cause.
	fake.failOn[key] = errors.New("injected: access denied")
	if f := inspectStateAccess(dir, auth.CredentialFiles()); f.Err == nil || !strings.Contains(f.Err.Error(), "injected: access denied") {
		t.Errorf("err = %v, want the cause of a read that failed for another reason", f.Err)
	}
}

// stateCheckedHook sits between the check on a name and the read of it, for
// each object, which is where a test replaces the object. The test that uses it
// on Windows proves the read is by handle; this one proves the seam is where it
// is said to be, so that test cannot pass by swapping at the wrong moment.
func TestStateCheckedHookRunsAfterTheCheckAndBeforeTheRead(t *testing.T) {
	fake := useFakeStateACL(t)
	dir := filepath.Join(t.TempDir(), "state")
	key := filepath.Join(dir, "dpop.key")
	writeFileT(t, key, "x")
	var events []string
	saved := stateCheckedHook
	stateCheckedHook = func(path string) { events = append(events, "hook "+path+" after reads "+strconv.Itoa(len(fake.read))) }
	t.Cleanup(func() { stateCheckedHook = saved })

	inspectStateAccess(dir, []string{"dpop.key", "absent.key"})
	want := []string{"hook " + dir + " after reads 0", "hook " + key + " after reads 1"}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %q, want %q: once per object that exists, each before its own read and after the previous one's", events, want)
	}
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// The check is part of doctor exactly where the platform manages access lists,
// and runs through doctor's own entry point. assembleDoctor is fed facts in the
// test above, but the line in cmdDoctor that gathers them is only reached by
// running the command; this is the test that goes red if it is deleted, on an
// OS whose real backend would never have run it.
func TestDoctorReportsStateAccessWhereAccessListsAreManaged(t *testing.T) {
	fake := useFakeStateACL(t)
	as := newFakeAS(t)
	root := t.TempDir()
	stateDir, spoolDir := filepath.Join(root, "state"), filepath.Join(root, "spool")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := doctorConfig(t, as.srv.URL, stateDir, spoolDir)
	fake.desc[stateDir] = winacl.Descriptor{Owner: stateTestUser, ACEs: []winacl.ACE{allowACE(stateTestUser, 0, stateFullControl), allowACE(stateTestLogon, aceInherited, stateReadExecute)}}
	before := listNames(t, stateDir)

	var out, errOut bytes.Buffer
	_ = cmdDoctor([]string{"-config", cfgPath, "-json"}, &out, &errOut)
	check := doctorCheckNamed(t, out.String(), "state access")
	if check["verdict"] != string(verdictOK) {
		t.Fatalf("verdict = %v (%v), want OK: another principal is information", check["verdict"], check["detail"])
	}
	detail, _ := check["detail"].(string)
	if !strings.Contains(detail, stateDir) || !strings.Contains(detail, `NT AUTHORITY\LogonSessionId_0_4242`) {
		t.Errorf("detail does not name the directory and who else it admits: %q", detail)
	}
	if _, has := check["fix"]; has && check["fix"] != "" {
		t.Errorf("an informational line carries a fix: %v", check["fix"])
	}
	if !reflect.DeepEqual(fake.read, []string{stateDir}) {
		t.Errorf("doctor read %v, want only the state directory: nothing else exists in it", fake.read)
	}
	if after := listNames(t, stateDir); !reflect.DeepEqual(before, after) {
		t.Errorf("doctor changed the state directory: %v -> %v", before, after)
	}

	var text bytes.Buffer
	_ = cmdDoctor([]string{"-config", cfgPath}, &text, &errOut)
	if !strings.Contains(text.String(), "state access") || !strings.Contains(text.String(), "also names") {
		t.Errorf("the text report does not carry the state access line:\n%s", text.String())
	}

	// A directory with no access list is a fact the participant did not intend.
	fake.desc[stateDir] = winacl.Descriptor{Owner: stateTestUser, NullDACL: true}
	out.Reset()
	_ = cmdDoctor([]string{"-config", cfgPath, "-json"}, &out, &errOut)
	check = doctorCheckNamed(t, out.String(), "state access")
	if check["verdict"] != string(verdictNo) || check["fix"] == nil || check["fix"] == "" {
		t.Errorf("a directory with no access list: verdict %v fix %v, want NO with a fix", check["verdict"], check["fix"])
	}
}

// Where access lists are not managed — every POSIX system, where file modes are
// the whole story — there is no state access check at all.
func TestDoctorHasNoStateAccessCheckWhereAccessListsAreNotManaged(t *testing.T) {
	if stateACL.managed {
		t.Skip("this platform manages access lists")
	}
	as := newFakeAS(t)
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := doctorConfig(t, as.srv.URL, stateDir, filepath.Join(root, "spool"))
	var out, errOut bytes.Buffer
	_ = cmdDoctor([]string{"-config", cfgPath}, &out, &errOut)
	if strings.Contains(out.String(), "state access") {
		t.Errorf("a state access line appeared where access lists are not managed:\n%s", out.String())
	}
	if checks := assembleDoctor(healthyFacts()); checks[len(checks)-1].Name == "state access" {
		t.Error("assembleDoctor added a state access check to facts that were not checked")
	}
}

// doctorCheckNamed decodes doctor -json and returns the check with that name.
func doctorCheckNamed(t *testing.T, jsonOut, name string) map[string]any {
	t.Helper()
	data := dataOf(t, decodeCommandEnvelope(t, jsonOut, "doctor"))
	checks, _ := data["checks"].([]any)
	for _, raw := range checks {
		if c, ok := raw.(map[string]any); ok && c["name"] == name {
			return c
		}
	}
	t.Fatalf("doctor -json has no %q check:\n%s", name, jsonOut)
	return nil
}
