package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLineageFileRoundTripAndWalkUp(t *testing.T) {
	fs, ops := newFakeHookOps(nil)
	dir := "/sessions"
	root := "/home/u/project"
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	ops.now = func() time.Time { return now }

	if err := updateLineage(ops, lineagePath(dir, root), now, func(l *lineageFile) {
		l.Harness, l.SessionID, l.Window = "cursor", "abc", "none"
		l.History = []traceHistory{{Role: "assistant", Text: "looking it up"}}
	}); err != nil {
		t.Fatal(err)
	}
	// The file name reveals nothing about the path.
	for name := range fs.files {
		if filepath.Base(name) == "project.json" || len(filepath.Base(name)) != 32+len(".json") {
			t.Errorf("lineage file name leaks or is unhashed: %s", name)
		}
	}
	got, path := lineageForCwd(ops, dir, filepath.Join(root, "src", "deep"), "cursor", "", now.Add(time.Minute))
	if got == nil || path != lineagePath(dir, root) || got.SessionID != "abc" {
		t.Fatalf("walk-up did not find the workspace lineage: %+v %q", got, path)
	}
	env := got.envelope()
	if env == nil || env.Harness != "cursor" || env.Window != "none" || len(env.History) != 1 {
		t.Errorf("envelope: %+v", env)
	}
	// Stale lineage is ignored rather than threading a new conversation
	// into an old one.
	if stale, _ := lineageForCwd(ops, dir, root, "cursor", "", now.Add(lineageMaxAge+time.Second)); stale != nil {
		t.Error("a stale lineage file was used")
	}
	// A corrupt file is absent.
	fs.files[lineagePath(dir, root)] = []byte("{corrupt")
	if l, ok := loadLineage(ops, lineagePath(dir, root)); ok || l != nil {
		t.Error("a corrupt lineage file was not treated as absent")
	}
}

func TestLineageHistoryIsCappedOnSave(t *testing.T) {
	_, ops := newFakeHookOps(nil)
	now := time.Now()
	path := lineagePath("/s", "/w")
	big := make([]byte, traceHistoryCap*2)
	for i := range big {
		big[i] = 'x'
	}
	big[len(big)-1] = 'E'
	if err := saveLineage(ops, path, &lineageFile{SessionID: "s", History: []traceHistory{{Role: "assistant", Text: string(big)}}}, now); err != nil {
		t.Fatal(err)
	}
	l, ok := loadLineage(ops, path)
	if !ok || len(l.History[0].Text) != traceHistoryCap || l.History[0].Text[traceHistoryCap-1] != 'E' {
		t.Fatalf("history not capped to the tail: ok=%v len=%d", ok, len(l.History[0].Text))
	}
}
