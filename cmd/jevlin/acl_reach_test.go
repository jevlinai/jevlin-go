package main

// Who may reach the access-list reader and the account namer.
//
// Reading a DACL, and above all resolving a SID to an account (which can reach
// a domain controller), belongs in the commands that report on a machine and
// nowhere on the path a search, a hook or a flush takes (AGENTS.md invariant
// 1). pkg/auth's own guard (perm_reads_test.go) holds the shared store to that.
// This holds the wrapper: inspectStateAccess, the stateACL backend, winacl.Read
// and winacl.PrincipalName appear only in the files that report, and a call
// added anywhere else — loadTraceKey, which every hookless search runs, is the
// example that stayed green before — is a finding until someone decides it
// belongs.
//
// It reads the sources of every non-test file in the package, whatever its
// build constraints. A reference is an identifier or a selector, so a call, a
// function value and a variable all count.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const winaclImport = "github.com/jevlinai/jevlin-go/internal/winacl"

// mayReachACLReader says whether a file may reference the reader and the namer.
func mayReachACLReader(name string) (ok bool, why string) {
	switch {
	case name == "doctor.go":
		return true, "doctor gathers the facts for its checks"
	case strings.HasPrefix(name, "state_acl"):
		return true, "the state access check and its platform half"
	case strings.HasPrefix(name, "wallet_acl"):
		return true, "the wallet access check and its platform half"
	case name == "setup_env_windows.go":
		return true, "ownedByCurrentUser names the owner in the message of a refusal at adoption, after the decision and off every search path"
	}
	return false, ""
}

// aclReachFindings returns a line for each reference in file name to the
// reader or the namer, or to a dot import that would hide one.
func aclReachFindings(fset *token.FileSet, name string, f *ast.File) []string {
	if ok, _ := mayReachACLReader(name); ok {
		return nil
	}
	var out []string
	add := func(pos token.Pos, what string) {
		out = append(out, fmt.Sprintf("%s:%d %s", name, fset.Position(pos).Line, what))
	}
	alias := map[string]bool{} // names this file calls the winacl package by
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if path != winaclImport {
			continue
		}
		switch {
		case imp.Name != nil && imp.Name.Name == ".":
			add(imp.Pos(), "dot-imports winacl, which hides what is called from it")
		case imp.Name != nil:
			alias[imp.Name.Name] = true
		default:
			alias["winacl"] = true
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			if n.Name == "inspectStateAccess" || n.Name == "stateACL" {
				add(n.Pos(), "references "+n.Name)
			}
		case *ast.SelectorExpr:
			if id, ok := n.X.(*ast.Ident); ok && alias[id.Name] && (n.Sel.Name == "Read" || n.Sel.Name == "PrincipalName") {
				add(n.Pos(), "calls winacl."+n.Sel.Name)
			}
		}
		return true
	})
	return out
}

func TestOnlyTheReportingFilesReachTheACLReaderAndNamer(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var scanned int
	var controls = map[string]int{} // references the allowed files must be seen to make
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for _, finding := range aclReachFindings(fset, name, f) {
			t.Errorf("%s: only doctor.go, state_acl*.go, wallet_acl*.go and setup_env_windows.go may reach the reader and the namer (invariant 1)", finding)
		}
		// Positive control: the scan finds the references where they are, so a
		// parser or pattern that sees nothing cannot pass for a clean tree.
		if name == "doctor.go" {
			ast.Inspect(f, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && (id.Name == "inspectStateAccess" || id.Name == "stateACL") {
					controls[id.Name]++
				}
				return true
			})
		}
	}
	if scanned < 50 {
		t.Fatalf("scanned %d source files; the package has far more, so the glob found the wrong directory", scanned)
	}
	for _, want := range []string{"inspectStateAccess", "stateACL"} {
		if controls[want] == 0 {
			t.Errorf("doctor.go no longer references %s: either the check is not wired or this scan cannot see a reference", want)
		}
	}
}

// The scan is held to the forms it claims to find, parsed from source, as the
// scan over pkg/auth is: the files it runs over contain none of them outside
// the allowed set, so a form it missed would pass without notice.
func TestTheACLReachScanSeesEveryFormItClaimsTo(t *testing.T) {
	cases := []struct {
		name, file, src string
		want            int
	}{
		{"a call to inspectStateAccess", "trace.go", "package main\nfunc loadTraceKey() { inspectStateAccess(\"d\", nil) }\n", 1},
		{"a read of the backend", "search.go", "package main\nfunc f() bool { return stateACL.managed }\n", 1},
		{"winacl.Read", "trace.go", "package main\nimport \"" + winaclImport + "\"\nfunc f() { winacl.Read(\"p\") }\n", 1},
		{"winacl.PrincipalName through an alias", "flush.go", "package main\nimport w \"" + winaclImport + "\"\nfunc f() { w.PrincipalName(\"s\") }\n", 1},
		{"a function value, not a call", "search.go", "package main\nimport \"" + winaclImport + "\"\nvar read = winacl.Read\n", 1},
		{"a dot import", "search.go", "package main\nimport . \"" + winaclImport + "\"\nfunc f() { Read(\"p\") }\n", 1},
		{"winacl.RestrictToOwner is not the reader", "setup.go", "package main\nimport \"" + winaclImport + "\"\nfunc f() { winacl.RestrictToOwner(\"p\", true) }\n", 0},
		{"winacl.CurrentUser is not the namer", "wallet_store.go", "package main\nimport \"" + winaclImport + "\"\nfunc f() { winacl.CurrentUser() }\n", 0},
		{"the same call in doctor.go", "doctor.go", "package main\nfunc f() { inspectStateAccess(\"d\", nil) }\n", 0},
		{"the same call in state_acl_windows.go", "state_acl_windows.go", "package main\nimport \"" + winaclImport + "\"\nvar r = winacl.Read\n", 0},
		{"the same call in wallet_acl_windows.go", "wallet_acl_windows.go", "package main\nimport \"" + winaclImport + "\"\nvar r = winacl.PrincipalName\n", 0},
	}
	for _, c := range cases {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, c.file, c.src, 0)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := aclReachFindings(fset, c.file, f); len(got) != c.want {
			t.Errorf("%s: findings = %q, want %d", c.name, got, c.want)
		}
	}
}
