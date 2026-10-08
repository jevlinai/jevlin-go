package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// storeRecords are the files the store reads and writes that hold no
// credential: decisions, markers and records of what happened. A name in the
// store that is neither here nor in CredentialFiles() fails the test below, so
// adding a state file is a decision about which it is, taken where doctor's
// list is: the forbidden state is not choosing.
var storeRecords = map[string]string{
	"mining_decision.json":      "the mining decision (invariant 10)",
	"payout.json":               "the payout address in force, which is public",
	"payout_binding_held.json":  "connect's read-before-declare note",
	"payout_declared.json":      "the payout address connect declared",
	"epoch_conflicts.json":      "epochs the AS reported as conflicting",
	"revoke_pending.json":       "a marker that a revocation is owed",
	"receipt-%d-%d.jws":         "an enrollment receipt, signed by the AS and kept as evidence",
	"registration_pending.json": "the registration journal, which lives in the installation directory beside credentials.json and not in the state dir (invariant 13)",
	"claim.json":                "the claim link and code, which live in the installation directory beside credentials.json and not in the state dir (invariant 12)",
}

// storeNamesFromVariables are the call sites that pass a name held in a
// variable, so the walk cannot read it off the call. Each is classified by
// hand, here, and a new one fails the test.
var storeNamesFromVariables = map[string]string{
	"health.go:MarkHealth":       "health records, one file per component: records",
	"health.go:LoadHealth":       "health records, one file per component: records",
	"store.go:SaveReceipt":       "receipt-%d-%d.jws: a record",
	"store.go:loadAddressRecord": "payout.json, payout_declared.json: a payout address, which is public: records",
}

// Every file name the store reads or writes through readSecret,
// createExclusive or saveStateFile is read off the source and must be a
// credential (CredentialFiles) or a record (storeRecords). The behavioral test
// beside CredentialFiles only calls the writers it names, so a new credential
// file would pass it; this walks every call.
func TestEveryFileTheStoreNamesIsACredentialOrARecord(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var parsed []*ast.File
	consts := map[string]string{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, f)
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if i < len(vs.Values) {
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							consts[id.Name], _ = strconv.Unquote(lit.Value)
						}
					}
				}
			}
		}
	}

	credentials := map[string]bool{}
	for _, c := range CredentialFiles() {
		credentials[c] = true
	}
	named := map[string]bool{}     // every name the walk resolved
	variable := map[string]bool{}  // file:func of every call it could not
	callers := map[string]string{} // name -> where, for the message
	writers := map[string]bool{"readSecret": true, "createExclusive": true, "saveStateFile": true}
	for _, f := range parsed {
		file := fset.Position(f.Pos()).Filename
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !writers[sel.Sel.Name] {
					return true
				}
				at := fset.Position(call.Pos())
				switch arg := call.Args[0].(type) {
				case *ast.BasicLit:
					v, _ := strconv.Unquote(arg.Value)
					named[v], callers[v] = true, at.String()
				case *ast.Ident:
					if v, ok := consts[arg.Name]; ok {
						named[v], callers[v] = true, at.String()
					} else {
						variable[file+":"+fd.Name.Name] = true
					}
				case *ast.CallExpr:
					// fmt.Sprintf("receipt-%d-%d.jws", …): the format is the name.
					if lit, ok := firstArg(arg).(*ast.BasicLit); ok {
						v, _ := strconv.Unquote(lit.Value)
						named[v], callers[v] = true, at.String()
					} else {
						variable[file+":"+fd.Name.Name] = true
					}
				default:
					variable[file+":"+fd.Name.Name] = true
				}
				return true
			})
		}
	}

	for name := range named {
		if !credentials[name] && storeRecords[name] == "" {
			t.Errorf("%s names %q, which is neither in CredentialFiles() nor in storeRecords: decide which it is", callers[name], name)
		}
	}
	for name := range credentials {
		if !named[name] {
			t.Errorf("CredentialFiles() lists %q, which no call in the store names: a rename left the list behind", name)
		}
	}
	for name := range storeRecords {
		if !named[name] {
			t.Errorf("storeRecords lists %q, which no call in the store names any more", name)
		}
	}
	var gotVar, wantVar []string
	for k := range variable {
		gotVar = append(gotVar, k)
	}
	for k := range storeNamesFromVariables {
		wantVar = append(wantVar, k)
	}
	sort.Strings(gotVar)
	sort.Strings(wantVar)
	if strings.Join(gotVar, ",") != strings.Join(wantVar, ",") {
		t.Errorf("calls that pass a file name held in a variable: %v, classified by hand: %v; a new one must be classified in storeNamesFromVariables", gotVar, wantVar)
	}
}

func firstArg(call *ast.CallExpr) ast.Expr {
	if len(call.Args) == 0 {
		return nil
	}
	return call.Args[0]
}
