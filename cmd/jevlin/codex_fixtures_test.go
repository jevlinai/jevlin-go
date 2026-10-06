package main

// What Codex sends a hook, and what it writes into config.toml once a
// participant approves one (issue #19).
//
// Every testdata/hook/codex-*.json is a payload Codex handed a
// recording-only hook on 2026-10-05: codex-cli 0.158.0 on Linux and 0.160.0
// on macOS through `codex exec`, and the desktop app for the files named
// `-app-`. The compaction files come from a run that lowered
// model_auto_compact_token_limit so `codex exec` compacted on its own. They
// are Codex's own bytes, key order and spacing included, with three
// changes: a home directory is /home/u or /Users/u, a working directory that
// named a person's project or a private temporary path is a placeholder, and
// the spawn-agent payload's `message` — an opaque blob carrying the spawned
// agent's task — is replaced. Nothing else is edited on disk. A case that
// needs another shape derives it in the open, as hook_caller_test.go does
// with Cursor's payloads.
//
// Two captured events are deliberately absent. UserPromptSubmit carries the
// prompt and PostToolUse the tool's output; this client installs neither,
// and neither belongs in its repository (invariant 2).
//
// testdata/codex/ holds Codex's config.toml after a hook-trust review. The
// Linux file is whole. The macOS one is DERIVED: the capture's block was
// written by this client's predecessor, in a file that also held the
// participant's project list, so it is reduced to the block and re-marked —
// the markers, the comment lines and the writable roots are this client's
// own — while the eleven [hooks.state."…"] tables Codex wrote inside it are
// kept byte for byte apart from the home directory.

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// codexPayloadFixture reads one captured Codex payload and refuses it unless
// it is shaped like what was measured: a JSON object naming its event and its
// session, whose transcript is one of Codex's rollout files. The one payload
// captured with a null transcript, the desktop app's SessionEnd, says so in
// its name. The check is this file's own, not the hook's: a fixture guard
// that read the production rule would loosen with it.
func codexPayloadFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(readHookFixture(t, name), &m); err != nil || m == nil {
		t.Fatalf("%s is not a JSON object: %v", name, err)
	}
	for _, k := range []string{"hook_event_name", "session_id"} {
		if s, _ := m[k].(string); s == "" {
			t.Fatalf("%s carries no %q: it is not a payload Codex was seen to send, and a test run against it proves nothing", name, k)
		}
	}
	raw, present := m["transcript_path"]
	switch tp := raw.(type) {
	case string:
		base := path.Base(filepath.ToSlash(tp))
		if !strings.HasPrefix(base, "rollout-") || !strings.HasSuffix(base, ".jsonl") {
			t.Fatalf("%s names the transcript %q, which is not one of Codex's rollout files", name, tp)
		}
	case nil:
		if !present || !strings.Contains(name, "null-transcript") {
			t.Fatalf("%s carries no rollout transcript and does not say it was captured without one", name)
		}
	default:
		t.Fatalf("%s has a transcript_path of type %T", name, raw)
	}
	return m
}

// codexConfigFixture is one of the config.toml captures, as text.
func codexConfigFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "codex", name)) // #nosec G304 -- a fixed testdata path this file builds
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return string(b)
}

// homeDirRe finds a home directory in a fixture. The only one allowed is the
// placeholder.
var homeDirRe = regexp.MustCompile(`/(?:home|Users)/([^/"\s]*)`)

// Every Codex payload fixture is one Codex was seen to send, names its event
// in its file name, is not one of the two events whose payload is the
// participant's words or a tool's output, and carries no home directory but
// the placeholder.
func TestCodexFixturesAreThePayloadsCodexSent(t *testing.T) {
	names, err := filepath.Glob(filepath.Join("testdata", "hook", "codex-*.json"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no Codex payload fixtures: %v", err)
	}
	events := map[string]bool{}
	for _, full := range names {
		name := filepath.Base(full)
		t.Run(name, func(t *testing.T) {
			m := codexPayloadFixture(t, name)
			event, _ := m["hook_event_name"].(string)
			events[event] = true
			if !strings.Contains(name, "-"+event+"-") && !strings.HasSuffix(name, "-"+event+".json") {
				t.Errorf("the file is named for another event than the %s it holds", event)
			}
			switch event {
			case "UserPromptSubmit", "PostToolUse":
				t.Errorf("a %s payload carries the participant's words or a tool's output and is never a fixture", event)
			}
			for _, m := range homeDirRe.FindAllStringSubmatch(string(readHookFixture(t, name)), -1) {
				if m[1] != "u" {
					t.Errorf("a home directory survived the scrub: %s", m[0])
				}
			}
		})
	}
	// The five events the adapter installs were each seen firing.
	for _, ev := range []string{"PreToolUse", "SessionStart", "PreCompact", "PostCompact", "Stop"} {
		if !events[ev] {
			t.Errorf("no captured %s payload among the fixtures", ev)
		}
	}
}

// hookTrustKeyRe is the shape of every key Codex wrote under hooks.state:
// the hooks.json path, the event in snake case, and two indexes.
var hookTrustKeyRe = regexp.MustCompile(`^(.+):([a-z_]+):([0-9]+):([0-9]+)$`)

// The two config.toml captures hold what Codex wrote on approval: one
// [hooks.state."<hooks.json>:<event>:<i>:<j>"] table per approved hook, each
// holding a trusted_hash and nothing else. Linux 0.158.0 also writes a bare
// [hooks.state] parent table; macOS 0.160.0 wrote its tables inside the
// marked block, which was last in the file.
func TestCodexConfigFixturesAreWhatCodexWrote(t *testing.T) {
	for _, c := range []struct {
		name      string
		approvals int
		bareTable bool
		marked    bool
	}{
		{"config-0.158.0-linux.after-trust.toml", 9, true, false},
		{"config-0.160.0-macos.after-trust.block.toml", 11, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			text := codexConfigFixture(t, c.name)
			for _, m := range homeDirRe.FindAllStringSubmatch(text, -1) {
				if m[1] != "u" {
					t.Errorf("a home directory survived the scrub: %s", m[0])
				}
			}
			var doc struct {
				Hooks struct {
					State map[string]map[string]any `toml:"state"`
				} `toml:"hooks"`
			}
			if _, err := toml.Decode(text, &doc); err != nil {
				t.Fatalf("does not decode as TOML: %v", err)
			}
			if len(doc.Hooks.State) != c.approvals {
				t.Fatalf("%d approvals on record, want %d", len(doc.Hooks.State), c.approvals)
			}
			for key, table := range doc.Hooks.State {
				if !hookTrustKeyRe.MatchString(key) {
					t.Errorf("key %q is not <hooks.json>:<event>:<i>:<j>", key)
				}
				hash, _ := table["trusted_hash"].(string)
				if len(table) != 1 || !strings.HasPrefix(hash, "sha256:") {
					t.Errorf("%s: %v, want exactly one sha256 trusted_hash", key, table)
				}
			}
			if got := strings.Contains(text, "\n[hooks.state]\n"); got != c.bareTable {
				t.Errorf("bare [hooks.state] table present = %v, want %v", got, c.bareTable)
			}
			if _, _, _, ok := markedRegion([]byte(text)); ok != c.marked {
				t.Errorf("a marked block present = %v, want %v", ok, c.marked)
			}
		})
	}
}
