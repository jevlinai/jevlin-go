package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// Every command's help has the one shape help.go describes: it begins with
// its usage line, shows an example, and fits in 80 columns, as the index
// does. A help that grew a paragraph past the edge of a terminal, or lost
// its usage line, fails here rather than in a participant's terminal.
func TestEveryHelpHasTheOneShape(t *testing.T) {
	names := append(append([]string{}, helpCommands...), "config", "hook")
	texts := map[string]string{"": usageText}
	for _, name := range names {
		texts[name] = helpOf(t, name)
	}
	for name, h := range texts {
		label := "jevlin help " + name
		if !strings.HasSuffix(h, "\n") {
			t.Errorf("%s does not end in a newline", label)
		}
		for i, line := range strings.Split(h, "\n") {
			if len(line) > 80 {
				t.Errorf("%s line %d is %d columns, over 80:\n%s", label, i+1, len(line), line)
			}
		}
		if name == "" || name == "config" {
			continue
		}
		if !strings.HasPrefix(h, "usage: jevlin "+name) {
			t.Errorf("%s does not begin with its usage line:\n%s", label, h)
		}
		if name != "hook" && !strings.Contains(h, "\nexample") {
			t.Errorf("%s shows no example:\n%s", label, h)
		}
	}
}

// The index names every command a person runs, once each, and nothing
// else: each command dispatch answers is in helpCommands, which the index
// lists, except the two a hook runs. The dispatch cases are read from
// main.go, so a command added there and left out of the help fails here.
func TestTheIndexNamesEveryCommand(t *testing.T) {
	internal := map[string]bool{"hook": true, "turn-end": true, "-version": true, "--version": true}
	listed := map[string]bool{}
	for _, name := range helpCommands {
		if strings.Count(usageText, "\n  "+name+" ") != 1 {
			t.Errorf("the index does not list %q exactly once", name)
		}
		listed[name] = true
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var dispatched int
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "dispatch" {
			return true
		}
		if !answersHelpFirst(fn) {
			t.Error("dispatch does not ask helpRequested before it runs a command")
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				name, _ := strconv.Unquote(lit.Value)
				dispatched++
				if !internal[name] && !listed[name] {
					t.Errorf("dispatch answers %q, which the help index does not list", name)
				}
			}
			return true
		})
		return false
	})
	if dispatched < len(helpCommands) {
		t.Fatalf("read %d dispatch cases from main.go, fewer than the %d commands listed", dispatched, len(helpCommands))
	}
}

// Help asked for, in any of its spellings, goes to stdout and exits 0:
// `jevlin help <name>`, `jevlin <name> -h` and `jevlin agents <sub> -h`.
// A topic there is no help for is a usage error, and the hook's own -h is
// left to the hook.
func TestHelpIsAnsweredOnStdoutWithExitZero(t *testing.T) {
	ask := func(name string, args ...string) (int, bool, string, string) {
		var out, errOut bytes.Buffer
		code, ok := helpRequested(name, args, &out, &errOut)
		return code, ok, out.String(), errOut.String()
	}
	for _, spelling := range []string{"help", "-h", "-help", "--help"} {
		if code, ok, out, _ := ask(spelling); !ok || code != exitOK || out != usageText {
			t.Errorf("jevlin %s: exit %d, answered %v, want the index on stdout", spelling, code, ok)
		}
	}
	for _, name := range append(append([]string{}, helpCommands...), "config") {
		want := helpOf(t, name)
		if code, ok, out, _ := ask("help", name); !ok || code != exitOK || out != want {
			t.Errorf("jevlin help %s: exit %d, answered %v, stdout %q", name, code, ok, out)
		}
		if name == "config" {
			continue
		}
		for _, flag := range []string{"-h", "-help", "--help"} {
			if code, ok, out, _ := ask(name, flag); !ok || code != exitOK || out != want {
				t.Errorf("jevlin %s %s: exit %d, answered %v, stdout %q", name, flag, code, ok, out)
			}
		}
	}
	if code, ok, out, _ := ask("agents", "status", "-h"); !ok || code != exitOK || out != agentsUsage {
		t.Errorf("jevlin agents status -h: exit %d, answered %v, stdout %q", code, ok, out)
	}
	if code, ok, out, errOut := ask("help", "nonesuch"); !ok || code != exitUsage || out != "" || !strings.Contains(errOut, "nonesuch") {
		t.Errorf("jevlin help nonesuch: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	for _, args := range [][]string{{"-h"}, {"codex", "-h"}} {
		if _, ok, out, _ := ask("hook", args...); ok || out != "" {
			t.Errorf("jevlin hook %v was answered as help, not left to the hook: %q", args, out)
		}
	}
	// A query word is not a flag: search runs these.
	if _, ok, _, _ := ask("search", "what", "-h", "means"); ok {
		t.Error("jevlin search what -h means was answered as help")
	}
}

// A flag that does not parse answers with the command's own help on
// stderr, never the flag package's listing of defaults.
func TestABadFlagShowsTheCommandsHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := statusMain([]string{"-nonesuch"}, &out, &errOut, func(string) string { return "" }); code != exitUsage {
		t.Fatalf("status -nonesuch: exit %d", code)
	}
	if !strings.Contains(errOut.String(), statusHelp) || strings.Contains(errOut.String(), "Usage of") {
		t.Errorf("status -nonesuch did not show status's help:\n%s", errOut.String())
	}
}

// answersHelpFirst: is dispatch's first statement
// `if code, ok := helpRequested(...); ok`? A help that a command ran past
// would reach the command's flag parser, which answers on stderr with exit 2.
func answersHelpFirst(fn *ast.FuncDecl) bool {
	if len(fn.Body.List) == 0 {
		return false
	}
	ifs, ok := fn.Body.List[0].(*ast.IfStmt)
	if !ok || ifs.Init == nil {
		return false
	}
	as, ok := ifs.Init.(*ast.AssignStmt)
	if !ok || len(as.Rhs) != 1 {
		return false
	}
	call, ok := as.Rhs[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == "helpRequested"
}
