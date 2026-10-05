package main

// What `agents status` says about Codex's approval of the hooks, and that
// nothing this client does ever writes one (issue #19).
//
// Codex runs a hook only once the participant has approved it, and an
// unapproved hook is silent: under `codex exec` it simply does not run. So
// "installed" is not the whole answer, and status adds what Codex's own
// record says — config.toml's [hooks.state."<hooks.json>:<event>:<i>:<j>"]
// tables — read, never written, and never more than that record says.

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"
)

// codexStatusMachine is a fake machine whose Codex home is the one the macOS
// approvals were captured under, with this installation's hooks in it.
func codexStatusMachine(t *testing.T) (*fakeMachine, agentOps, agentPaths, binEntry) {
	t.Helper()
	m, ops := newFakeMachine()
	paths := ops.paths(envOf(map[string]string{"CODEX_HOME": "/Users/u/.codex"}))
	entry := goldenEntry()
	spec, err := codexHooks(entry, shellPOSIX)
	if err != nil {
		t.Fatal(err)
	}
	var p agentPlan
	if !planHooksMerge(ops, "Codex", paths.codexHooks, &p, entry, spec) {
		t.Fatal("no hooks planned")
	}
	if failures := commitPlan(ops, &p, io.Discard, io.Discard); failures != 0 {
		t.Fatalf("committing the hooks: %d failures", failures)
	}
	return m, ops, paths, entry
}

// approvalTables is config.toml text approving each event's first hook in
// hooksPath, as Codex writes it.
func approvalTables(hooksPath, index string, events ...string) string {
	var b strings.Builder
	for _, ev := range events {
		b.WriteString("\n[hooks.state.\"" + strings.ReplaceAll(hooksPath, `\`, `\\`) + ":" + ev + ":" + index + ":0\"]\ntrusted_hash = \"sha256:00\"\n")
	}
	return b.String()
}

func TestCodexStatusReportsApprovalOnRecordOnly(t *testing.T) {
	for _, c := range []struct {
		name   string
		config func(paths agentPaths) (string, bool) // the config.toml, and whether there is one
		hooks  func(t *testing.T, m *fakeMachine, paths agentPaths)
		want   string
	}{
		{"no config.toml at all", func(agentPaths) (string, bool) { return "", false }, nil,
			"hooks: no approval on record; Codex runs them only after you approve them in Codex"},
		{"a config.toml with no approval in it", func(agentPaths) (string, bool) { return "model = \"gpt-5\"\n", true }, nil,
			"hooks: no approval on record; Codex runs them only after you approve them in Codex"},
		{"the approvals Codex wrote on macOS, all five of ours among them", func(agentPaths) (string, bool) {
			return codexConfigFixture(t, macosAfterTrustBlock), true
		}, nil, "hooks: an approval is on record for 5 of 5; whether it is for the commands as they read now is Codex's to decide"},
		{"two of the five", func(p agentPaths) (string, bool) {
			return approvalTables(p.codexHooks, "0", "pre_tool_use", "stop"), true
		}, nil, "hooks: an approval is on record for 2 of 5; Codex runs the others only after you approve them in Codex"},
		{"approvals for another hooks.json", func(agentPaths) (string, bool) {
			return approvalTables("/Users/u/elsewhere/.codex/hooks.json", "0", "pre_tool_use", "session_start", "pre_compact", "post_compact", "stop"), true
		}, nil, "hooks: no approval on record; Codex runs them only after you approve them in Codex"},
		{"approvals for the second place in each event", func(p agentPaths) (string, bool) {
			return approvalTables(p.codexHooks, "1", "pre_tool_use", "session_start", "pre_compact", "post_compact", "stop"), true
		}, nil, "hooks: no approval on record; Codex runs them only after you approve them in Codex"},
		{"a config.toml that is not TOML", func(agentPaths) (string, bool) { return "= = =\n", true }, nil,
			"hooks: approval unknown; /Users/u/.codex/config.toml does not read as TOML"},
		{"a participant's hook listed before ours", func(p agentPaths) (string, bool) {
			return codexConfigFixture(t, macosAfterTrustBlock), true
		}, func(t *testing.T, m *fakeMachine, p agentPaths) {
			lists := codexEventLists(t, m.files[p.codexHooks])
			lists["PreToolUse"] = append([]any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "audit"}}}}, lists["PreToolUse"]...)
			m.files[p.codexHooks] = mustJSON(t, map[string]any{"hooks": lists})
		}, "hooks: approval unknown; jevlin's PreToolUse hook is not the first listed for that event, and how Codex numbers a later one is not established"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, ops, paths, entry := codexStatusMachine(t)
			if text, ok := c.config(paths); ok {
				m.files[paths.codexConfig] = []byte(text)
			}
			if c.hooks != nil {
				c.hooks(t, m, paths)
			}
			got := codexApprovalLines(ops, paths, entry)
			if len(got) != 1 || slash(got[0]) != c.want {
				t.Fatalf("status says %q\nwant %q", got, c.want)
			}
			for _, word := range []string{"active", "trusted", "enabled"} {
				if strings.Contains(got[0], word) {
					t.Errorf("status claims the hooks are %s; the record says only that an approval exists: %q", word, got[0])
				}
			}
		})
	}
	// No hook of ours in the file: nothing to say.
	_, ops := newFakeMachine()
	if got := codexApprovalLines(ops, ops.paths(noEnv), goldenEntry()); got != nil {
		t.Errorf("with no hooks of ours, status says %q", got)
	}
}

// The line, under Codex's row in `agents status`, on an OS where the hooks
// are written; on Windows, where none are, there is no line to print.
func TestCodexStatusPrintsTheApprovalUnderItsRow(t *testing.T) {
	_, ops := newFakeMachine("codex")
	if code, out, errOut := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes", "-client", "codex"); code != exitOK {
		t.Fatalf("install: %d\n%s%s", code, out, errOut)
	}
	var out bytes.Buffer
	printAgentStatus(ops, ops.paths(noEnv), goldenEntry(), nil, noEnv, &out)
	line := "  " + strings.Repeat(" ", 12) + " hooks: no approval on record; Codex runs them only after you approve them in Codex\n"
	if got := strings.Contains(out.String(), line); got != codexServesHere() {
		t.Errorf("approval line present = %v, want %v:\n%s", got, codexServesHere(), out.String())
	}
}

// The client never writes Codex's record of an approval, on any path that
// touches config.toml or hooks.json: an install, a second install, a refresh
// of an entry of ours that has gone stale, and an uninstall. The record
// Codex wrote is decoded after every step and must be what it was; where
// there was none, there is still none.
func TestTheClientNeverWritesCodexHookTrust(t *testing.T) {
	linux := codexConfigFixture(t, "config-0.158.0-linux.after-trust.toml")
	for _, c := range []struct{ name, config string }{
		{"Codex's approvals already on record", linux},
		{"none on record", "model = \"gpt-5\"\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfgPath, _ := sandboxTestConfig(t)
			m, ops := newFakeMachine("codex")
			paths := ops.paths(noEnv)
			m.files[paths.codexConfig] = []byte(c.config)
			want := hookTrustOf(t, c.config)
			check := func(step string) {
				t.Helper()
				if got := hookTrustOf(t, string(m.files[paths.codexConfig])); !reflect.DeepEqual(got, want) {
					t.Fatalf("after %s, Codex's record of approvals is\n%v\nwant\n%v", step, got, want)
				}
				if strings.Contains(string(m.files[paths.codexHooks]), "trusted_hash") {
					t.Fatalf("after %s, hooks.json carries an approval", step)
				}
			}
			for _, step := range []string{"install", "a second install"} {
				if code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
					t.Fatalf("%s: %d\n%s%s", step, code, out, errOut)
				}
				check(step)
			}
			if b, ok := m.files[paths.codexHooks]; ok {
				// An entry of ours spelled as an earlier renderer might
				// have, so the next install rewrites it.
				stale := bytes.ReplaceAll(b, []byte("'"), []byte(`\"`))
				m.files[paths.codexHooks] = stale
				if code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
					t.Fatalf("refresh: %d\n%s%s", code, out, errOut)
				}
				if bytes.Equal(m.files[paths.codexHooks], stale) || !bytes.Equal(m.files[paths.codexHooks], b) {
					t.Fatal("the refresh did not rewrite the stale entries back to what install writes, so this step proves nothing")
				}
				check("a refresh of a stale entry")
			}
			if code, out, errOut := runAgents(t, ops, nil, "uninstall", "-config", cfgPath, "-yes"); code != exitOK {
				t.Fatalf("uninstall: %d\n%s%s", code, out, errOut)
			}
			check("uninstall")
		})
	}
}
