package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The Claude Code skill's file form, run with the real binary in bash
// exactly as its note spells it: the one-line search with `<<< '...'`
// replaced by `< '<file>'`. A request holding every character the one-line
// call cannot carry (an apostrophe, a `$`, a backtick, a command
// substitution) reaches the router byte for byte.
func TestTheRequestFileCarriesWhatTheOneLineCallCannot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Claude Code's file form is the POSIX call; its Windows form is held by the one-line tests")
	}
	in := newExecInstallation(t)
	block := skillBlockFor(t, in.renderedSkill("claude"), shellPOSIX, "search")
	head, _, ok := strings.Cut(block.body, " <<< ")
	if !ok {
		t.Fatalf("not the one-line form: %s", block.body)
	}
	dir := requestsDirFor(in.entry.cfg)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const query = "what's the $HOME price `id` $(id)"
	body, _ := json.Marshal(map[string]any{"version": 1, "query": query})
	file := filepath.Join(dir, "q-4821.json")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	out := runInShell(t, shellBash, head+" < "+posixQuoteArg(file), nil, in.env)
	got := requireOneRequest(t, in, out, query)
	var sent map[string]any
	if err := json.Unmarshal(got.Raw, &sent); err != nil || sent["query"] != query {
		t.Errorf("the router got %v (%v)", sent["query"], err)
	}
}
