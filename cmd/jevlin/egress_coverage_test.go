package main

// Every request that carries a credential goes through internal/egress. This
// test reads every non-test source file in the module and finds each place an
// Authorization or DPoP header is set, or Basic authorization added: each must
// be in a function classified below, with the guard its request goes through,
// and that function must reach the guard. A new one fails here until someone
// classifies it: the forbidden state is not choosing.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// credentialHeaderSites is every function that puts a credential on a
// request, and the call in that function that reaches the egress guard. The
// DPoP transport sets its proof below x/oauth2 and the AS clients, and is
// itself wrapped in egress.Transport where it is built (newOAuthClient, held
// by TestTheOAuthTransportIsWrappedInTheGuard).
var credentialHeaderSites = map[string]string{
	"cmd/jevlin/credentials.go probeKey":                "egress.Do",
	"cmd/jevlin/search.go postRouter":                   "egress.Do",
	"cmd/jevlin/limits.go cmdLimits":                    "egress.Do",
	"cmd/jevlin/turn_end.go sendTurnEnd":                "egress.Do",
	"pkg/platform/client.go Client.Status":              "egress.Do",
	"pkg/platform/client.go Client.Me":                  "egress.Do",
	"pkg/platform/client.go Client.ClaimCode":           "egress.Do",
	"pkg/platform/client.go Client.Enroll":              "egress.Do",
	"pkg/auth/joinclient.go MiningClient.authedRequest": "newCredentialClient",
	"pkg/auth/submit.go Submitter.Submit":               "newCredentialClient",
	"pkg/auth/transport.go dpopTransport.attempt":       "t.base.RoundTrip",
}

// credentialHeaders are the header names that carry a credential here.
var credentialHeaders = map[string]bool{"Authorization": true, "DPoP": true}

func TestEveryCredentialHeaderIsSetBehindTheEgressGuard(t *testing.T) {
	root := moduleRoot(t)
	found := map[string]bool{}
	for _, dir := range []string{"cmd", "pkg", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				name := funcName(fn)
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if !setsCredential(call) {
						return true
					}
					site := rel + " " + name
					found[site] = true
					guard, classified := credentialHeaderSites[site]
					if !classified {
						t.Errorf("%s sets a credential on a request and is not classified: send it through egress.Do "+
							"or a client whose transport is egress.Transport, then add it to credentialHeaderSites", site)
						return true
					}
					if !callsGuard(fn.Body, guard) {
						t.Errorf("%s sets a credential and is classified as sending through %s, which it does not call", site, guard)
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for site := range credentialHeaderSites {
		if !found[site] {
			t.Errorf("credentialHeaderSites names %s, which sets no credential header any more: remove it", site)
		}
	}
}

// The DPoP transport and the AS clients' token traffic are held by the
// transport newOAuthClient builds: egress.Transport around the DPoP one.
func TestTheOAuthTransportIsWrappedInTheGuard(t *testing.T) {
	path := filepath.Join(moduleRoot(t), "pkg", "auth", "oauthclient.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "newOAuthClient" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "transport" {
				if u, ok := kv.Value.(*ast.UnaryExpr); ok {
					if lit, ok := u.X.(*ast.CompositeLit); ok {
						if sel, ok := lit.Type.(*ast.SelectorExpr); ok && exprString(sel) == "egress.Transport" {
							wrapped = true
						}
					}
				}
			}
			return true
		})
	}
	if !wrapped {
		t.Fatal("newOAuthClient's transport is not an egress.Transport: every DPoP proof, token and assertion would reach whatever origin a request names")
	}
}

func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// setsCredential: is call x.Header.Set/Add of a credential header, or
// x.SetBasicAuth?
func setsCredential(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name == "SetBasicAuth" {
		return true
	}
	if sel.Sel.Name != "Set" && sel.Sel.Name != "Add" {
		return false
	}
	if inner, ok := sel.X.(*ast.SelectorExpr); !ok || inner.Sel.Name != "Header" {
		return false
	}
	if len(call.Args) == 0 {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	name, err := strconv.Unquote(lit.Value)
	return err == nil && credentialHeaders[name]
}

// callsGuard: does body contain a call whose function reads as guard?
func callsGuard(body *ast.BlockStmt, guard string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && exprString(call.Fun) == guard {
			found = true
		}
		return !found
	})
	return found
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	}
	return ""
}
