package auth

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Nothing in this package reads an access list or resolves an account name.
// Every state read in a search goes through this package, so a DACL read here
// is a DACL read on the earning path (AGENTS.md invariant 1), and a refusal
// here turns mining DEGRADED for an installation whose agent sandbox was
// granted the state directory on purpose (perm.go).
//
// The Windows half of the package cannot be compiled on the OS that runs most
// of the tests, and a behavioral test of it runs only on the Windows legs, so
// this reads the sources instead: every file in the package, whatever its build
// constraints, since the parser does not apply them. What it permits is the one
// call the package needs from internal/winacl, which sets a list and reads none.
const (
	windowsPkg   = "golang.org/x/sys/windows"
	winaclPkg    = "github.com/jevlinai/jevlin-go/internal/winacl"
	allowedWinfn = "RestrictToOwner"
)

// Reading a security descriptor, by name or handle.
var readsDescriptor = map[string]bool{
	"GetNamedSecurityInfo": true, "GetSecurityInfo": true, "GetKernelObjectSecurity": true, "GetFileSecurity": true,
}

// Turning a SID into an account, or the reverse, or asking the system about a
// user. These are matched by name whatever the receiver or package, since
// LookupAccount is a method on a SID and LookupSID exists in both syscall and
// x/sys/windows.
var resolvesAccount = map[string]bool{
	"LookupAccount": true, "LookupAccountSid": true, "LookupAccountName": true, "LookupSID": true,
	"TranslateName": true, "TranslateAccountName": true, "NetUserGetInfo": true,
}

// accessListFindings returns one line per way the file reads an access list,
// resolves an account, or hides that it does.
func accessListFindings(fset *token.FileSet, f *ast.File) []string {
	var out []string
	add := func(pos token.Pos, format string, args ...any) {
		at := fset.Position(pos)
		out = append(out, fmt.Sprintf("%s:%d %s", at.Filename, at.Line, fmt.Sprintf(format, args...)))
	}
	local := map[string]string{} // the name a file refers to an import by -> its path
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		switch {
		case imp.Name != nil && imp.Name.Name == ".":
			// A dot import puts the names in scope unqualified, where a walk
			// for selectors cannot see them.
			add(imp.Pos(), "dot-imports %s, which hides what is called from it", path)
		case imp.Name != nil:
			local[imp.Name.Name] = path
		default:
			local[path[strings.LastIndex(path, "/")+1:]] = path
		}
		if path == "os/user" {
			add(imp.Pos(), "imports os/user, which resolves the current user and accounts by name")
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if resolvesAccount[sel.Sel.Name] {
			add(sel.Pos(), "resolves an account name (%s)", sel.Sel.Name)
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch local[id.Name] {
		case windowsPkg:
			if readsDescriptor[sel.Sel.Name] {
				add(sel.Pos(), "reads a security descriptor (windows.%s)", sel.Sel.Name)
			}
		case winaclPkg:
			if sel.Sel.Name != allowedWinfn {
				add(sel.Pos(), "uses winacl.%s; only winacl.%s may be used here", sel.Sel.Name, allowedWinfn)
			}
		}
		return true
	})
	return out
}

func TestThePackageReadsNoAccessListAndResolvesNoAccount(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var scanned int
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for _, finding := range accessListFindings(fset, f) {
			t.Error(finding)
		}
	}
	if scanned < 10 {
		t.Fatalf("scanned %d source files; the package has more than that, so the glob found the wrong directory", scanned)
	}
}

// The scan is only as good as what it recognizes, and the files it runs over
// contain none of these, so a form it missed would pass for years. Each way a
// read or a lookup can be written is parsed from source here and must be found;
// the one permitted call must not be.
func TestTheAccessListScanSeesEveryFormItClaimsTo(t *testing.T) {
	const header = "package auth\n"
	cases := []struct {
		name string
		src  string
		want []string // substrings, one finding each
	}{
		{"a descriptor read by name", header + "import \"golang.org/x/sys/windows\"\nfunc f() { windows.GetNamedSecurityInfo(\"p\", 0, 0) }\n", []string{"reads a security descriptor (windows.GetNamedSecurityInfo)"}},
		{"a descriptor read by handle", header + "import \"golang.org/x/sys/windows\"\nfunc f() { windows.GetSecurityInfo(0, 0, 0) }\n", []string{"windows.GetSecurityInfo"}},
		{"through an alias", header + "import w \"golang.org/x/sys/windows\"\nfunc f() { w.GetSecurityInfo(0, 0, 0) }\n", []string{"windows.GetSecurityInfo"}},
		{"a SID turned into an account", header + "func f(sid interface{ LookupAccount(string) }) { sid.LookupAccount(\"\") }\n", []string{"resolves an account name (LookupAccount)"}},
		{"LookupSID", header + "import \"golang.org/x/sys/windows\"\nfunc f() { windows.LookupSID(\"\", \"\") }\n", []string{"(LookupSID)"}},
		{"TranslateName", header + "import \"golang.org/x/sys/windows\"\nfunc f() { windows.TranslateName(nil, 0, 0, nil, nil) }\n", []string{"(TranslateName)"}},
		{"TranslateAccountName", header + "import \"golang.org/x/sys/windows\"\nfunc f() { windows.TranslateAccountName(\"\", 0, 0, 0) }\n", []string{"(TranslateAccountName)"}},
		{"NetUserGetInfo", header + "import \"golang.org/x/sys/windows\"\nfunc f() { windows.NetUserGetInfo(nil, nil, 0, nil) }\n", []string{"(NetUserGetInfo)"}},
		{"os/user", header + "import \"os/user\"\nfunc f() { user.Current() }\n", []string{"imports os/user"}},
		{"os/user under another name", header + "import u \"os/user\"\nfunc f() { u.Current() }\n", []string{"imports os/user"}},
		{"a dot import of the windows package", header + "import . \"golang.org/x/sys/windows\"\nfunc f() { GetNamedSecurityInfo(\"p\", 0, 0) }\n", []string{"dot-imports golang.org/x/sys/windows"}},
		{"a dot import of winacl", header + "import . \"github.com/jevlinai/jevlin-go/internal/winacl\"\nfunc f() { Read(\"p\") }\n", []string{"dot-imports github.com/jevlinai/jevlin-go/internal/winacl"}},
		{"winacl.Read", header + "import \"github.com/jevlinai/jevlin-go/internal/winacl\"\nfunc f() { winacl.Read(\"p\") }\n", []string{"winacl.Read"}},
		{"winacl.PrincipalName under an alias", header + "import acl \"github.com/jevlinai/jevlin-go/internal/winacl\"\nfunc f() { acl.PrincipalName(\"s\") }\n", []string{"winacl.PrincipalName"}},
		{"the one permitted call", header + "import \"github.com/jevlinai/jevlin-go/internal/winacl\"\nfunc f() { winacl.RestrictToOwner(\"p\", true) }\n", nil},
		{"windows calls that read nothing", header + "import \"golang.org/x/sys/windows\"\nfunc f() { windows.ACLFromEntries(nil, nil); windows.SetNamedSecurityInfo(\"p\", 0, 0, nil, nil, nil, nil) }\n", nil},
	}
	for _, c := range cases {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "synthetic.go", c.src, 0)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := accessListFindings(fset, f)
		sort.Strings(got)
		if len(got) != len(c.want) {
			t.Errorf("%s: findings = %q, want %d containing %q", c.name, got, len(c.want), c.want)
			continue
		}
		for i, want := range c.want {
			if !strings.Contains(got[i], want) {
				t.Errorf("%s: finding %d = %q, want it to contain %q", c.name, i, got[i], want)
			}
		}
	}
}
