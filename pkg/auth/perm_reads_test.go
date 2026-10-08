package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
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
func TestThePackageReadsNoAccessListAndResolvesNoAccount(t *testing.T) {
	const (
		windowsPkg   = "golang.org/x/sys/windows"
		winaclPkg    = "github.com/jevlinai/jevlin-go/internal/winacl"
		allowedWinfn = "RestrictToOwner"
	)
	// Reading a security descriptor, by name or handle, and turning a SID into
	// an account. LookupAccount is a method on a SID, so it is matched by name
	// whatever the receiver.
	readsDescriptor := map[string]bool{"GetNamedSecurityInfo": true, "GetSecurityInfo": true, "GetKernelObjectSecurity": true}
	resolvesAccount := map[string]bool{"LookupAccount": true, "LookupAccountSid": true, "LookupAccountName": true}

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
		local := map[string]string{} // the name a file refers to an import by -> its path
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			switch {
			case imp.Name != nil:
				local[imp.Name.Name] = path
			default:
				local[path[strings.LastIndex(path, "/")+1:]] = path
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			at := fset.Position(sel.Pos())
			if resolvesAccount[sel.Sel.Name] {
				t.Errorf("%s:%d resolves an account name (%s)", at.Filename, at.Line, sel.Sel.Name)
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch local[id.Name] {
			case windowsPkg:
				if readsDescriptor[sel.Sel.Name] {
					t.Errorf("%s:%d reads a security descriptor (windows.%s)", at.Filename, at.Line, sel.Sel.Name)
				}
			case winaclPkg:
				if sel.Sel.Name != allowedWinfn {
					t.Errorf("%s:%d uses winacl.%s; only winacl.%s may be used here", at.Filename, at.Line, sel.Sel.Name, allowedWinfn)
				}
			}
			return true
		})
	}
	if scanned < 10 {
		t.Fatalf("scanned %d source files; the package has more than that, so the glob found the wrong directory", scanned)
	}
}
