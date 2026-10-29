package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// Every prompt for a secret reads it with readSecret, which turns echo off at
// a terminal; a bufio line read there would put the secret on the screen.
func TestEverySecretPromptReadsWithoutEcho(t *testing.T) {
	path := filepath.Join(moduleRoot(t), "cmd", "jevlin", "enroll.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "cmdEnroll" {
			continue
		}
		reads, secret := false, false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "readSecret" {
				secret = true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "ReadString" || sel.Sel.Name == "ReadLine") {
				reads = true
			}
			return true
		})
		if reads || !secret {
			t.Errorf("cmdEnroll reads the enrollment token with a line read (%v) instead of readSecret (%v)", reads, secret)
		}
		return
	}
	t.Fatal("cmdEnroll not found")
}
