package main

// The agents command against an in-memory machine: which files it writes,
// what it merges into a host's own config, and that uninstall removes
// exactly what install wrote and nothing else.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/internal/selfupdate"
)

type fakeFileInfo struct {
	name string
	size int64
	dir  bool
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() os.FileMode  { return 0o600 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) Sys() any           { return nil }

type fakeMachine struct {
	files    map[string][]byte
	onPath   map[string]string
	removed  []string
	terminal bool
}

// slash keys every path the same way on every OS: ops.paths joins with
// the host separator, the tests write forward slashes, and Windows would
// otherwise see two different files.
func slash(p string) string { return filepath.ToSlash(p) }

func newFakeMachine(onPath ...string) (*fakeMachine, agentOps) {
	m := &fakeMachine{files: map[string][]byte{}, onPath: map[string]string{}}
	for _, p := range onPath {
		m.onPath[p] = "/usr/local/bin/" + p
	}
	ops := agentOps{
		home: "/home/u",
		lookPath: func(name string) (string, error) {
			if p, ok := m.onPath[name]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		executable:     func() (string, error) { return "/home/u/.jevlin/bin/jevlin", nil },
		binaryLocation: func(string) ([]string, error) { return nil, nil },
		readFile: func(p string) ([]byte, error) {
			b, ok := m.files[slash(p)]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return b, nil
		},
		writeFile: func(p string, b []byte, _ os.FileMode) error { m.files[slash(p)] = b; return nil },
		mkdirAll:  func(string, os.FileMode) error { return nil },
		stat: func(p string) (os.FileInfo, error) {
			p = slash(p)
			if _, ok := m.files[p]; ok {
				return fakeFileInfo{name: filepath.Base(p)}, nil
			}
			for f := range m.files {
				if strings.HasPrefix(f, p+"/") {
					return fakeFileInfo{name: filepath.Base(p), dir: true}, nil
				}
			}
			return nil, fs.ErrNotExist
		},
		removeAll: func(p string) error {
			p = slash(p)
			m.removed = append(m.removed, p)
			for f := range m.files {
				if f == p || strings.HasPrefix(f, p+"/") {
					delete(m.files, f)
				}
			}
			return nil
		},
		remove: func(p string) error {
			p = slash(p)
			for f := range m.files {
				if strings.HasPrefix(f, p+"/") {
					return errors.New("remove " + p + ": directory not empty")
				}
			}
			if _, ok := m.files[p]; !ok {
				return fs.ErrNotExist
			}
			delete(m.files, p)
			m.removed = append(m.removed, p)
			return nil
		},
		isTerminal: func() bool { return m.terminal },
	}
	return m, ops
}

func runAgents(t *testing.T, ops agentOps, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := agentsMain(ops, args, strings.NewReader(""), &out, &errOut, envOf(env))
	return code, out.String(), errOut.String()
}

const testCfg = "/home/u/.jevlin/jevlin.toml"

func hooksOf(t *testing.T, m *fakeMachine, path string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(m.files[path], &doc); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, m.files[path])
	}
	h, _ := doc["hooks"].(map[string]any)
	return h
}

func allowOf(t *testing.T, m *fakeMachine, path string) []string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(m.files[path], &doc); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, m.files[path])
	}
	perms, _ := doc["permissions"].(map[string]any)
	list, _ := perms["allow"].([]any)
	var out []string
	for _, e := range list {
		out = append(out, e.(string))
	}
	return out
}

func TestAgentsDryRunDetectsAgentsAndWritesNothing(t *testing.T) {
	m, ops := newFakeMachine("claude", "cursor")
	code, out, _ := runAgents(t, ops, nil, "install", "-config", testCfg, "-dry-run")
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	out = filepath.ToSlash(out)
	for _, want := range []string{"agents: Claude Code, Cursor", "skills/jevlin/SKILL.md", "settings.json", "hooks.json", "(dry run"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if len(m.files) != 0 {
		t.Errorf("dry run wrote: %v", keysOf(m.files))
	}
}

func TestAgentsInstallWritesClaudeSkillAndMergesHooksIntoSettings(t *testing.T) {
	m, ops := newFakeMachine("claude")
	settings := "/home/u/.claude/settings.json"
	m.files[settings] = []byte(`{"model":"opus","permissions":{"allow":["Bash(git status:*)"]},"hooks":{"PreToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"lint"}]}]}}`)
	code, out, errOut := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes")
	if code != exitOK {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	skill := string(m.files["/home/u/.claude/skills/jevlin/SKILL.md"])
	// The paths are single-quoted from H2 on: that is POSIX quoting, where
	// nothing expands, rather than Go's %q, where $ and ` still do.
	if !strings.Contains(skill, `'/home/u/.jevlin/bin/jevlin' search -config '`) || !strings.Contains(skill, `jevlin.toml' -format model`) || !strings.Contains(skill, "name: jevlin") {
		t.Errorf("skill:\n%s", skill)
	}
	var doc map[string]any
	_ = json.Unmarshal(m.files[settings], &doc)
	if doc["model"] != "opus" {
		t.Error("an unrelated setting was lost")
	}
	hooks := hooksOf(t, m, settings)
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 2 || pre[0].(map[string]any)["matcher"] != "Write" {
		t.Fatalf("existing PreToolUse group not preserved first: %v", pre)
	}
	ours := pre[1].(map[string]any)
	// The matcher covers both shell tools from H3 on (dropin-miner#77): a search the model
	// sends through Claude Code's PowerShell tool reaches this hook too. The
	// value is written out rather than compared with the constant — a test
	// that reads the constant agrees with whatever the constant becomes.
	if ours["matcher"] != "Bash|PowerShell" || !strings.Contains(ours["hooks"].([]any)[0].(map[string]any)["command"].(string), `jevlin.toml' lineage`) {
		t.Errorf("our PreToolUse group: %v", ours)
	}
	for _, ev := range []string{"SessionStart", "PreCompact", "PostCompact", "Stop"} {
		if _, ok := hooks[ev]; !ok {
			t.Errorf("no %s hook", ev)
		}
	}
	if bytes.Contains(m.files[settings], []byte("sr-")) || bytes.Contains(m.files[settings], []byte("JEVLIN_API_KEY")) {
		t.Error("a key reached settings.json")
	}
	allow := allowOf(t, m, settings)
	if len(allow) != 9 || allow[0] != "Bash(git status:*)" {
		t.Fatalf("the user's own allow rule must come first, then our three spellings of the search and three of the fetch, and the requests folder's Edit and Read rules: %v", allow)
	}
	// The config path is absolutized by the host (a drive letter on Windows),
	// so match around it rather than on it.
	for i, want := range []struct{ prefix, suffix string }{
		{`Bash('/home/u/.jevlin/bin/jevlin' search -config '`, `jevlin.toml':*)`},
		{`Bash("/home/u/.jevlin/bin/jevlin" search -config "`, `jevlin.toml":*)`},
		{`Bash(/home/u/.jevlin/bin/jevlin search -config "`, `jevlin.toml":*)`},
		// The fetch has no human form: each rule ends at --stdin, where the
		// request on stdin begins, and allows nothing the skill does not
		// render.
		{`Bash('/home/u/.jevlin/bin/jevlin' fetch -config '`, `jevlin.toml' --stdin:*)`},
		{`Bash("/home/u/.jevlin/bin/jevlin" fetch -config "`, `jevlin.toml" --stdin:*)`},
		{`Bash(/home/u/.jevlin/bin/jevlin fetch -config "`, `jevlin.toml" --stdin:*)`},
	} {
		r := allow[i+1]
		if !strings.HasPrefix(r, want.prefix) || !strings.HasSuffix(r, want.suffix) {
			t.Errorf("allow rule %d: %s", i+1, r)
		}
	}
	if allow[7] != "Edit(//home/u/.jevlin/requests/**)" || allow[8] != "Read(//home/u/.jevlin/requests/**)" {
		t.Errorf("the requests folder's rules: %s, %s", allow[7], allow[8])
	}
	for _, r := range allow[1:] {
		if strings.Contains(r, "hook") || strings.Contains(r, "flush") || strings.Contains(r, "wallet") {
			t.Errorf("a rule allows more than search and fetch: %s", r)
		}
	}

	// Idempotent: a second install changes nothing.
	before := string(m.files[settings])
	code, out, _ = runAgents(t, ops, nil, "install", "-config", testCfg, "-yes")
	if code != exitOK || !strings.Contains(out, "already installed") || string(m.files[settings]) != before {
		t.Errorf("second install: exit %d\n%s", code, out)
	}
}

func TestAgentsInstallWritesCursorSkillAndHooksFileWithVersion(t *testing.T) {
	m, ops := newFakeMachine("cursor")
	code, out, _ := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes")
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, ok := m.files["/home/u/.cursor/skills/jevlin/SKILL.md"]; !ok {
		t.Error("no cursor skill")
	}
	hooksPath := "/home/u/.cursor/hooks.json"
	var doc map[string]any
	if err := json.Unmarshal(m.files[hooksPath], &doc); err != nil {
		t.Fatalf("hooks.json: %v", err)
	}
	if doc["version"] != float64(1) {
		t.Errorf("version: %v", doc["version"])
	}
	hooks := hooksOf(t, m, hooksPath)
	for _, ev := range []string{"sessionStart", "beforeShellExecution", "afterAgentThought", "afterAgentResponse", "preCompact", "stop"} {
		list, _ := hooks[ev].([]any)
		if len(list) != 1 {
			t.Errorf("%s: %v", ev, list)
			continue
		}
		cmd := list[0].(map[string]any)["command"].(string)
		if !strings.Contains(cmd, "hook -config") || !strings.HasSuffix(cmd, "cursor "+ev) {
			t.Errorf("%s command: %q", ev, cmd)
		}
	}
}

func TestAgentsInstallRefusesAHooksFileItCannotParse(t *testing.T) {
	m, ops := newFakeMachine("cursor")
	m.files["/home/u/.cursor/hooks.json"] = []byte("{ // a comment\n \"version\": 1 }")
	code, out, _ := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes")
	if code != exitTransport || !strings.Contains(out, "refused: Cursor") || !strings.Contains(out, "not plain JSON") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !bytes.Contains(m.files["/home/u/.cursor/hooks.json"], []byte("// a comment")) {
		t.Error("the file was rewritten")
	}
	if _, ok := m.files["/home/u/.cursor/skills/jevlin/SKILL.md"]; !ok {
		t.Error("the skill, which needed no merge, was withheld")
	}
}

func TestAgentsUninstallRemovesOnlyWhatInstallWrote(t *testing.T) {
	m, ops := newFakeMachine("claude", "cursor", "codex", "opencode")
	m.files["/home/u/.claude/settings.json"] = []byte(`{"permissions":{"allow":["Bash(git status:*)"]},"hooks":{"PreToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"lint"}]}],"Stop":[{"hooks":[{"type":"command","command":"say done"}]}]}}`)
	m.files["/home/u/.cursor/hooks.json"] = []byte(`{"version":1,"hooks":{"stop":[{"command":"./hooks/mine.sh"}]}}`)
	m.files["/home/u/.codex/hooks.json"] = []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"say done","timeout":5}]}]}}`)
	if code, out, _ := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes"); code != exitOK {
		t.Fatalf("install: %d\n%s", code, out)
	}
	code, out, _ := runAgents(t, ops, nil, "uninstall", "-config", testCfg, "-yes")
	if code != exitOK {
		t.Fatalf("uninstall: %d\n%s", code, out)
	}
	for _, gone := range []string{
		"/home/u/.claude/skills/jevlin/SKILL.md",
		"/home/u/.cursor/skills/jevlin/SKILL.md",
		"/home/u/.codex/skills/jevlin/SKILL.md",
		"/home/u/.config/opencode/plugins/jevlin.js",
	} {
		if _, ok := m.files[gone]; ok {
			t.Errorf("still present: %s", gone)
		}
	}
	claude := hooksOf(t, m, "/home/u/.claude/settings.json")
	if pre := claude["PreToolUse"].([]any); len(pre) != 1 || pre[0].(map[string]any)["matcher"] != "Write" {
		t.Errorf("Claude PreToolUse after uninstall: %v", pre)
	}
	if stop := claude["Stop"].([]any); len(stop) != 1 {
		t.Errorf("the user's own Stop hook was touched: %v", stop)
	}
	if allow := allowOf(t, m, "/home/u/.claude/settings.json"); len(allow) != 1 || allow[0] != "Bash(git status:*)" {
		t.Errorf("allow rules after uninstall: %v", allow)
	}
	for _, ev := range []string{"SessionStart", "PreCompact", "PostCompact"} {
		if _, ok := claude[ev]; ok {
			t.Errorf("Claude %s not removed", ev)
		}
	}
	cursor := hooksOf(t, m, "/home/u/.cursor/hooks.json")
	if stop := cursor["stop"].([]any); len(stop) != 1 || stop[0].(map[string]any)["command"] != "./hooks/mine.sh" {
		t.Errorf("the user's own Cursor stop hook was touched: %v", stop)
	}
	if _, ok := cursor["sessionStart"]; ok {
		t.Error("Cursor sessionStart not removed")
	}
	codex := hooksOf(t, m, "/home/u/.codex/hooks.json")
	if stop := codex["Stop"].([]any); len(stop) != 1 || len(codex) != 1 {
		t.Errorf("Codex's hooks after uninstall: %v, want only the participant's own Stop hook", codex)
	}
	for _, r := range m.removed {
		if !strings.Contains(r, agentsName) {
			t.Errorf("removed something not ours: %s", r)
		}
	}
}

func TestAgentsPreferOffRewritesSkillsAndInstallKeepsIt(t *testing.T) {
	m, ops := newFakeMachine("claude", "codex")
	claudeSkill := "/home/u/.claude/skills/jevlin/SKILL.md"
	codexSkill := "/home/u/.codex/skills/jevlin/SKILL.md"
	if code, out, _ := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes"); code != exitOK {
		t.Fatalf("install: %d\n%s", code, out)
	}
	on := string(m.files[claudeSkill])
	// The config path is host-absolutized (a drive letter on Windows), so
	// match around it.
	if !strings.Contains(on, "Prefer it over a built-in web search") || !strings.Contains(on, `' agents prefer -config '`) || !strings.Contains(on, `jevlin.toml' <argument>`) {
		t.Fatalf("shipped skill should prefer the router and name the prefer command:\n%s", on)
	}
	if code, out, _ := runAgents(t, ops, nil, "status", "-config", testCfg); code != exitOK || !strings.Contains(out, "search default: on") {
		t.Errorf("status before: %d %q", code, out)
	}

	code, out, errOut := runAgents(t, ops, nil, "prefer", "off", "-config", testCfg)
	if code != exitOK || !strings.Contains(out, "search default: off") || !strings.Contains(out, "Claude Code, Codex") {
		t.Fatalf("prefer off: %d\n%s%s", code, out, errOut)
	}
	prefFiles := 0
	for p, b := range m.files {
		if strings.HasSuffix(p, "/.jevlin/search-default") {
			prefFiles++
			if got := strings.TrimSpace(string(b)); got != "builtin" {
				t.Errorf("preference file: %q", got)
			}
		}
	}
	if prefFiles != 1 {
		t.Errorf("want one preference file beside the config, found %d in %v", prefFiles, keysOf(m.files))
	}
	for _, p := range []string{claudeSkill, codexSkill} {
		off := string(m.files[p])
		if strings.Contains(off, "Prefer it over a built-in web search") || !strings.Contains(off, "turned OFF as the default") || !strings.Contains(off, "Use the agent's built-in web") {
			t.Errorf("%s not rewritten for off:\n%s", p, off)
		}
		if !strings.Contains(off, `'/home/u/.jevlin/bin/jevlin' search -config '`) {
			t.Errorf("%s lost the search command", p)
		}
	}
	if _, ok := m.files["/home/u/.cursor/skills/jevlin/SKILL.md"]; ok {
		t.Error("prefer created a skill for an agent that had none")
	}

	// A reinstall keeps the choice; it is the user's.
	if code, out, _ := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes"); code != exitOK {
		t.Fatalf("reinstall: %d\n%s", code, out)
	}
	if !strings.Contains(string(m.files[claudeSkill]), "turned OFF as the default") {
		t.Error("reinstall reverted the preference")
	}
	if code, out, _ := runAgents(t, ops, nil, "status", "-config", testCfg); !strings.Contains(out, "search default: off") {
		t.Errorf("status after: %d %q", code, out)
	}

	code, out, _ = runAgents(t, ops, nil, "prefer", "on", "-config", testCfg)
	if code != exitOK || !strings.Contains(out, "search default: on") || string(m.files[claudeSkill]) != on {
		t.Errorf("prefer on did not restore the shipped skill: %d %q", code, out)
	}
	if code, _, _ := runAgents(t, ops, nil, "prefer", "maybe", "-config", testCfg); code != exitUsage {
		t.Errorf("bad argument: %d", code)
	}
}

func TestAgentsRefusesToWriteWithoutATerminalOrYes(t *testing.T) {
	m, ops := newFakeMachine("claude")
	code, _, errOut := runAgents(t, ops, nil, "install", "-config", testCfg)
	if code != exitUsage || !strings.Contains(errOut, "-yes") || len(m.files) != 0 {
		t.Fatalf("exit %d err %q files %v", code, errOut, keysOf(m.files))
	}
}

func TestAgentsPrintsRulesWhenNoAgentIsFoundAndClientOverridesDetection(t *testing.T) {
	m, ops := newFakeMachine()
	code, out, _ := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes")
	// The snippet a participant pastes into AGENTS.md now teaches the
	// structured path, so the phrase asserted here moved with it.
	if code != exitOK || !strings.Contains(out, "no coding agent found") ||
		!strings.Contains(out, "send one JSON request on stdin") || !strings.Contains(out, "--stdin") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	code, out, _ = runAgents(t, ops, nil, "install", "-config", testCfg, "-yes", "-client", "codex")
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, ok := m.files["/home/u/.codex/skills/jevlin/SKILL.md"]; !ok {
		t.Error("-client codex did not install the codex skill")
	}
	if !strings.Contains(out, "sandboxed") {
		t.Error("the Codex network caveat was not printed")
	}
}

func TestAgentsStatusReportsPathAndInstallState(t *testing.T) {
	m, ops := newFakeMachine("cursor")
	_, out, _ := runAgents(t, ops, nil, "status", "-config", testCfg)
	if !strings.Contains(out, "Cursor       found: cursor on PATH      not installed") || !strings.Contains(out, "Claude Code  not found                  not installed") {
		t.Fatalf("status:\n%s", out)
	}
	_, _, _ = runAgents(t, ops, nil, "install", "-config", testCfg, "-yes")
	_, out, _ = runAgents(t, ops, nil, "status", "-config", testCfg)
	if !strings.Contains(out, "Cursor       found: cursor on PATH      installed (skill+hooks)") {
		t.Fatalf("status after install:\n%s", out)
	}
	_ = m
}

func TestAgentsOpencodePluginRewritesOurCommandOnly(t *testing.T) {
	m, ops := newFakeMachine("opencode")
	if code, out, _ := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes"); code != exitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	js := string(m.files["/home/u/.config/opencode/plugins/jevlin.js"])
	for _, want := range []string{`input.tool !== "bash"`, "JEVLIN_TRACE_BRIDGE=", "tokendrop-trace-v1|", "export const JevlinLineage"} {
		if !strings.Contains(js, want) {
			t.Errorf("plugin lacks %q", want)
		}
	}
	if strings.Contains(js, "{{") {
		t.Error("an unexpanded placeholder is in the plugin")
	}
}

// TestAgentsUninstallSparesAHookEntryForADifferentInstallationOfTheSameName
// probes the boundary TestAgentsUninstallRemovesOnlyWhatInstallWrote does
// not: entryIsOurs/ruleIsOurs decide "ours" by strings.Contains(command,
// bin), where bin is THIS process's full absolute path (ops.executable()).
// A hook or allow rule left by a DIFFERENT jevlin installation — a
// stale entry from before a reinstall moved the binary, or a second copy
// entirely, sharing only the basename — must survive uninstall exactly
// like any other foreign entry. If the match were ever loosened to the
// basename alone, this is the entry that would start disappearing.
func TestAgentsUninstallSparesAHookEntryForADifferentInstallationOfTheSameName(t *testing.T) {
	m, ops := newFakeMachine("claude")
	other := "/opt/other-vendor/jevlin"
	m.files["/home/u/.claude/settings.json"] = []byte(fmt.Sprintf(`{
		"permissions":{"allow":["Bash(%s search:*)"]},
		"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":%q}]}]}
	}`, other, other+" hook lineage"))
	if code, out, _ := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes"); code != exitOK {
		t.Fatalf("install: %d\n%s", code, out)
	}
	code, out, _ := runAgents(t, ops, nil, "uninstall", "-config", testCfg, "-yes")
	if code != exitOK {
		t.Fatalf("uninstall: %d\n%s", code, out)
	}
	claude := hooksOf(t, m, "/home/u/.claude/settings.json")
	pre, _ := claude["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("a different installation's PreToolUse group was removed: %v", pre)
	}
	if got := pre[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"]; got != other+" hook lineage" {
		t.Errorf("the foreign group survived but was altered: %v", got)
	}
	allow := allowOf(t, m, "/home/u/.claude/settings.json")
	found := false
	for _, r := range allow {
		if strings.Contains(r, other) {
			found = true
		}
	}
	if !found {
		t.Errorf("a different installation's allow rule was removed: %v", allow)
	}
}

// TestAgentsHookAndAllowRuleMatchingSurvivesAWindowsStyleBinaryPath guards
// entryIsOurs and ruleIsOurs against the defect found on PR dropin-miner#51's Windows
// run: both used strings.Contains(command, bin), but binEntry's commands
// and claudeAllowRules' quoted rule are written with %q, which doubles
// every backslash. On a Windows path like the one used here, bin's own
// single-backslash bytes never occur as a contiguous run inside the
// quoted text, so the substring test never matched — a second install
// duplicated every Claude and Cursor hook, and uninstall removed none of
// them (nor the quoted half of the Claude allow rules). This runs on
// every OS: the path is only ever a string embedded in JSON, nothing
// executes it.
func TestAgentsHookAndAllowRuleMatchingSurvivesAWindowsStyleBinaryPath(t *testing.T) {
	m, ops := newFakeMachine("claude", "cursor")
	ops.executable = func() (string, error) { return `C:\Users\u\.jevlin\bin\jevlin.exe`, nil }

	for i := 1; i <= 2; i++ {
		if code, out, errOut := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes"); code != exitOK {
			t.Fatalf("install #%d: exit %d\n%s%s", i, code, out, errOut)
		}
	}

	settings := "/home/u/.claude/settings.json"
	claudeHooksAfter := hooksOf(t, m, settings)
	for _, ev := range []string{"PreToolUse", "SessionStart", "PreCompact", "PostCompact", "Stop"} {
		list, _ := claudeHooksAfter[ev].([]any)
		if len(list) != 1 {
			t.Errorf("Claude %s after two installs: want 1 entry, got %d: %v", ev, len(list), list)
		}
	}
	// Three spellings of each prefix rule, the search's and the fetch's, and
	// the requests folder's Edit and Read rules, and still eight after a
	// second install: the single-quoted path the skill renders, and the
	// %q-quoted and bare ones an agent may type instead.
	if allow := allowOf(t, m, settings); len(allow) != 8 {
		t.Fatalf("Claude allow rules after two installs: want 8, got %d: %v", len(allow), allow)
	}

	cursorPath := "/home/u/.cursor/hooks.json"
	cursorHooksAfter := hooksOf(t, m, cursorPath)
	for _, ev := range []string{"sessionStart", "beforeShellExecution", "afterAgentThought", "afterAgentResponse", "preCompact", "stop"} {
		list, _ := cursorHooksAfter[ev].([]any)
		if len(list) != 1 {
			t.Errorf("Cursor %s after two installs: want 1 entry, got %d: %v", ev, len(list), list)
		}
	}

	if code, out, errOut := runAgents(t, ops, nil, "uninstall", "-config", testCfg, "-yes"); code != exitOK {
		t.Fatalf("uninstall: exit %d\n%s%s", code, out, errOut)
	}
	// Both files held nothing but what this client wrote — every hook entry,
	// every allow rule, and the `version` key install adds — so from H5 they
	// are removed rather than left as an empty shell. Leaving ~/.cursor/
	// hooks.json behind as `{"hooks":{},"version":1}` is what made a machine
	// that never had Cursor detect as Cursor forever after (H4's leftover).
	for _, path := range []string{settings, cursorPath} {
		if _, ok := m.files[path]; ok {
			t.Errorf("%s held nothing but this installation's entries and was not removed: %s", path, m.files[path])
		}
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// wantHookCommand is what this client would write for tg's hook event on
// this OS, through the same renderer the installer uses.
func wantHookCommand(t *testing.T, tg installTarget, e binEntry, sub ...string) string {
	t.Helper()
	shells, err := declaredShells(tg, runtime.GOOS, channelHook)
	if err != nil {
		t.Fatalf("%s hook shells on %s: %v", tg.ID(), runtime.GOOS, err)
	}
	cmd, _, err := e.hookCommandForRunners(shells, sub...)
	if err != nil {
		t.Fatalf("%s hook command for %v: %v", tg.ID(), shells, err)
	}
	return cmd
}

// TestEveryHookSpellingIsReplacedOnInstallAndRemovedOnUninstall seeds both
// hook files with an entry in every spelling the recognizer accepts as ours,
// then requires install to leave exactly one — the current rendering — and
// uninstall to leave none.
//
// Every other test installs into a file this client wrote itself, so the
// recognizer is only ever fed the current spelling. A %q entry is the one
// that matters: it is ours by the recognizer, and it does not parse in
// PowerShell (dropin-miner#69), so an install that saw its own binary in it and decided
// there was nothing to do would leave a hook that never runs.
//
// The Windows-style path is deliberate: with a POSIX path, %q and the cmd
// double-quoted form are the same string and would not be two spellings.
// Nothing here executes the path; it is a string inside JSON on every OS.
func TestEveryHookSpellingIsReplacedOnInstallAndRemovedOnUninstall(t *testing.T) {
	const bin = `C:\Users\u\.jevlin\bin\jevlin.exe`
	// The installer resolves -config with filepath.Abs before it renders
	// anything, so on Windows the path it writes is rooted on the runner's
	// current drive (D:\home\u\... on the CI image) and is spelled with
	// backslashes. The expectation has to come from the same resolution
	// rather than from the literal flag value: building it from the literal
	// is how the first version of this test failed on both Windows runners
	// while the part it exists to check — the quoting — was already right.
	cfg, err := filepath.Abs(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	entry := binEntry{command: bin, cfg: cfg}
	spellings := func(sub string) []string {
		return []string{
			strconv.Quote(bin) + " hook -config " + strconv.Quote(cfg) + " " + sub,
			bin + " hook -config " + cfg + " " + sub,
			posixQuoteArg(bin) + " hook -config " + posixQuoteArg(cfg) + " " + sub,
			"& " + powerShellQuoteArg(bin) + " hook -config " + powerShellQuoteArg(cfg) + " " + sub,
			`"` + bin + `" hook -config "` + cfg + `" ` + sub,
		}
	}
	// Five spellings must be five distinct strings, or this test is weaker
	// than it reads.
	seen := map[string]bool{}
	for _, s := range spellings("flush") {
		if seen[s] {
			t.Fatalf("two spellings are the same string: %q", s)
		}
		seen[s] = true
	}

	m, ops := newFakeMachine("claude", "cursor")
	ops.executable = func() (string, error) { return bin, nil }

	claudeStop := []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "say done"}}}}
	for _, c := range spellings("flush") {
		claudeStop = append(claudeStop, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": c}}})
	}
	cursorStop := []any{map[string]any{"command": "./hooks/mine.sh"}}
	for _, c := range spellings("cursor stop") {
		cursorStop = append(cursorStop, map[string]any{"command": c})
	}
	seed := func(path string, doc map[string]any) {
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		m.files[path] = b
	}
	const settings, cursorPath = "/home/u/.claude/settings.json", "/home/u/.cursor/hooks.json"
	seed(settings, map[string]any{"hooks": map[string]any{"Stop": claudeStop}})
	seed(cursorPath, map[string]any{"version": 1, "hooks": map[string]any{"stop": cursorStop}})

	claude, _ := targetByID(installTargets, "claude")
	cursor, _ := targetByID(installTargets, "cursor")

	// Twice: the first install replaces five stale entries with one, the
	// second must find nothing left to do.
	for i := 1; i <= 2; i++ {
		if code, out, errOut := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes"); code != exitOK {
			t.Fatalf("install #%d: exit %d\n%s%s", i, code, out, errOut)
		}
		for _, c := range []struct {
			what, path, event string
			want, user        string
			command           func(any) string
		}{
			{"Claude", settings, "Stop", wantHookCommand(t, claude, entry, "flush"), "say done", claudeGroupCommand},
			{"Cursor", cursorPath, "stop", wantHookCommand(t, cursor, entry, "cursor", "stop"), "./hooks/mine.sh", cursorEntryCommand},
		} {
			list, _ := hooksOf(t, m, c.path)[c.event].([]any)
			if len(list) != 2 {
				t.Fatalf("%s %s after install #%d: want the user's entry and exactly one of ours, got %d: %v",
					c.what, c.event, i, len(list), list)
			}
			if got := c.command(list[0]); got != c.user {
				t.Errorf("%s %s: the user's own entry was disturbed: %q", c.what, c.event, got)
			}
			if got := c.command(list[1]); got != c.want {
				t.Errorf("%s %s after install #%d:\n got %q\nwant %q", c.what, c.event, i, got, c.want)
			}
		}
	}

	if code, out, errOut := runAgents(t, ops, nil, "uninstall", "-config", testCfg, "-yes"); code != exitOK {
		t.Fatalf("uninstall: exit %d\n%s%s", code, out, errOut)
	}
	for _, c := range []struct {
		what, path, event, user string
		command                 func(any) string
	}{
		{"Claude", settings, "Stop", "say done", claudeGroupCommand},
		{"Cursor", cursorPath, "stop", "./hooks/mine.sh", cursorEntryCommand},
	} {
		list, _ := hooksOf(t, m, c.path)[c.event].([]any)
		if len(list) != 1 || c.command(list[0]) != c.user {
			t.Errorf("%s %s after uninstall: want only the user's %q, got %v", c.what, c.event, c.user, list)
		}
	}
}

func claudeGroupCommand(e any) string {
	g, _ := e.(map[string]any)
	hs, _ := g["hooks"].([]any)
	if len(hs) == 0 {
		return ""
	}
	h, _ := hs[0].(map[string]any)
	s, _ := h["command"].(string)
	return s
}

func cursorEntryCommand(e any) string {
	h, _ := e.(map[string]any)
	s, _ := h["command"].(string)
	return s
}

// TestInstallReplacesOneStaleEntryPerEventInPlace: exactly ONE entry of ours
// per event, in the %q spelling no hook is rendered in, plus two of the allow
// rules install writes.
//
// TestEveryHookSpellingIsReplacedOnInstallAndRemovedOnUninstall cannot see
// what this sees. It seeds every spelling at once, so the merge loop replaces
// because it counted more than one entry of ours — never because it compared
// the one it found against what it would write now. A sameJSONValue that
// answered "the same" for any two entries leaves that test green and leaves
// a stale entry that does not parse in PowerShell (dropin-miner#69). Here there is exactly one entry and it is stale, so the
// replacement happens only if the comparison is real.
//
// The Windows-style binary path makes the stale spelling differ from the
// current rendering on every runner: %q doubles the backslashes, and no
// shell's quoting does.
func TestInstallReplacesOneStaleEntryPerEventInPlace(t *testing.T) {
	const bin = `C:\Users\u\.jevlin\bin\jevlin.exe`
	cfg, err := filepath.Abs(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	entry := binEntry{command: bin, cfg: cfg}
	stale := func(sub ...string) string {
		return strconv.Quote(bin) + " hook -config " + strconv.Quote(cfg) + " " + strings.Join(sub, " ")
	}

	claudeEvents := []struct {
		event string
		sub   []string
	}{
		{"PreToolUse", []string{"lineage"}},
		{"SessionStart", []string{"window", "session-start"}},
		{"PreCompact", []string{"window", "pre-compact"}},
		{"PostCompact", []string{"window", "post-compact"}},
		{"Stop", []string{"flush"}},
	}
	cursorEvents := []string{"sessionStart", "beforeShellExecution", "afterAgentThought", "afterAgentResponse", "preCompact", "stop"}

	m, ops := newFakeMachine("claude", "cursor")
	ops.executable = func() (string, error) { return bin, nil }

	// A stale settings.json: one group per event, PreToolUse matching Bash
	// alone, and one hook of the participant's own on two events.
	claudeHooksSeed := map[string]any{}
	for _, ce := range claudeEvents {
		group := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": stale(ce.sub...)}}}
		if ce.event == "PreToolUse" {
			group["matcher"] = "Bash"
		}
		list := []any{group}
		if ce.event == "PreToolUse" || ce.event == "Stop" {
			list = append(list, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "the participant's own " + ce.event}}})
		}
		claudeHooksSeed[ce.event] = list
	}
	cursorHooksSeed := map[string]any{}
	for _, ev := range cursorEvents {
		list := []any{map[string]any{"command": stale("cursor", ev)}}
		if ev == "stop" {
			list = append(list, map[string]any{"command": "./hooks/mine.sh"})
		}
		cursorHooksSeed[ev] = list
	}
	seed := func(path string, doc map[string]any) {
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		m.files[path] = b
	}
	const settings, cursorPath = "/home/u/.claude/settings.json", "/home/u/.cursor/hooks.json"
	seed(settings, map[string]any{
		"hooks": claudeHooksSeed,
		"permissions": map[string]any{"allow": []any{
			fmt.Sprintf("Bash(%q search -config %q:*)", bin, cfg),
			fmt.Sprintf("Bash(%s search -config %q:*)", bin, cfg),
			"Bash(git status:*)",
		}},
	})
	seed(cursorPath, map[string]any{"version": 1, "hooks": cursorHooksSeed})

	if code, out, errOut := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes"); code != exitOK {
		t.Fatalf("install: exit %d\n%s%s", code, out, errOut)
	}

	claude, _ := targetByID(installTargets, "claude")
	cursor, _ := targetByID(installTargets, "cursor")

	claudeAfter := hooksOf(t, m, settings)
	for _, ce := range claudeEvents {
		list, _ := claudeAfter[ce.event].([]any)
		want := wantHookCommand(t, claude, entry, ce.sub...)
		var ours, foreign int
		for _, e := range list {
			switch got := claudeGroupCommand(e); {
			case got == want:
				ours++
			case got == stale(ce.sub...):
				t.Errorf("Claude %s: the stale entry was left exactly as it was: %q", ce.event, got)
			default:
				foreign++
			}
		}
		if ours != 1 {
			t.Errorf("Claude %s: %d entries carry the current rendering %q, want 1: %v", ce.event, ours, want, list)
		}
		wantForeign := 0
		if ce.event == "PreToolUse" || ce.event == "Stop" {
			wantForeign = 1
		}
		if foreign != wantForeign {
			t.Errorf("Claude %s: %d foreign entries, want %d: %v", ce.event, foreign, wantForeign, list)
		}
	}
	// The replaced PreToolUse group carries the matcher this version writes,
	// not the stale Bash-only one (dropin-miner#77).
	for _, e := range claudeAfter["PreToolUse"].([]any) {
		g, _ := e.(map[string]any)
		if claudeGroupCommand(e) == wantHookCommand(t, claude, entry, "lineage") && g["matcher"] != claudeToolMatcher {
			t.Errorf("the replaced PreToolUse group still matches %v", g["matcher"])
		}
	}

	cursorAfter := hooksOf(t, m, cursorPath)
	for _, ev := range cursorEvents {
		list, _ := cursorAfter[ev].([]any)
		want := wantHookCommand(t, cursor, entry, "cursor", ev)
		var ours, foreign int
		for _, e := range list {
			switch got := cursorEntryCommand(e); {
			case got == want:
				ours++
			case got == stale("cursor", ev):
				t.Errorf("Cursor %s: the stale entry was left exactly as it was: %q", ev, got)
			default:
				foreign++
			}
		}
		if ours != 1 {
			t.Errorf("Cursor %s: %d entries carry the current rendering %q, want 1: %v", ev, ours, want, list)
		}
		wantForeign := 0
		if ev == "stop" {
			wantForeign = 1
		}
		if foreign != wantForeign {
			t.Errorf("Cursor %s: %d foreign entries, want %d: %v", ev, foreign, wantForeign, list)
		}
	}

	// Every rule this version writes is present, the participant's own is
	// untouched, and nothing is duplicated.
	allow := allowOf(t, m, settings)
	for _, rule := range claudeAllowRules(entry) {
		if n := countString(allow, rule); n != 1 {
			t.Errorf("allow rule %q appears %d times, want 1: %v", rule, n, allow)
		}
	}
	if countString(allow, "Bash(git status:*)") != 1 {
		t.Errorf("the participant's own allow rule was disturbed: %v", allow)
	}

	// And the upgrade settles: a second install writes nothing at all.
	before, beforeCursor := string(m.files[settings]), string(m.files[cursorPath])
	if code, out, errOut := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes"); code != exitOK {
		t.Fatalf("second install: exit %d\n%s%s", code, out, errOut)
	}
	if string(m.files[settings]) != before {
		t.Errorf("a second install rewrote settings.json:\n got %s\nwant %s", m.files[settings], before)
	}
	if string(m.files[cursorPath]) != beforeCursor {
		t.Errorf("a second install rewrote hooks.json:\n got %s\nwant %s", m.files[cursorPath], beforeCursor)
	}
}

func countString(list []string, want string) int {
	n := 0
	for _, s := range list {
		if s == want {
			n++
		}
	}
	return n
}

// agents tier records the participant's search band beside the config and
// rewrites nothing: the skill tells the agent to leave `tier` unset, so the
// file is read by `search`, not rendered into any host. default removes
// the file; a word the router would refuse is refused here.
func TestAgentsTierWritesOneFileBesideTheConfigAndRewritesNoSkill(t *testing.T) {
	m, ops := newFakeMachine("claude", "codex")
	claudeSkill := "/home/u/.claude/skills/jevlin/SKILL.md"
	// The config path is host-absolutized (a drive letter on Windows), so
	// the file beside it is found by suffix, as the prefer test finds its.
	tierFile := func() (string, bool) {
		for p, b := range m.files {
			if strings.HasSuffix(p, "/.jevlin/search-tier") {
				return strings.TrimSpace(string(b)), true
			}
		}
		return "", false
	}
	if code, out, _ := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes"); code != exitOK {
		t.Fatalf("install: %d\n%s", code, out)
	}
	before := string(m.files[claudeSkill])
	if !strings.Contains(before, `' agents tier -config '`) || !strings.Contains(before, `jevlin.toml' <tier>`) {
		t.Fatalf("the shipped skill does not name the tier command:\n%s", before)
	}
	if code, out, _ := runAgents(t, ops, nil, "status", "-config", testCfg); code != exitOK || !strings.Contains(out, "search tier:    unset") {
		t.Errorf("status before: %d %q", code, out)
	}
	if code, out, _ := runAgents(t, ops, nil, "tier", "-config", testCfg); code != exitOK || !strings.Contains(out, "search tier: unset") {
		t.Errorf("tier (status) before: %d %q", code, out)
	}

	code, out, errOut := runAgents(t, ops, nil, "tier", "balanced", "-config", testCfg)
	if code != exitOK || !strings.Contains(out, "search tier: balanced") || !strings.Contains(out, "next search") {
		t.Fatalf("tier balanced: %d\n%s%s", code, out, errOut)
	}
	if got, ok := tierFile(); !ok || got != "balanced" {
		t.Fatalf("tier file: %q ok=%v in %v", got, ok, keysOf(m.files))
	}
	if string(m.files[claudeSkill]) != before {
		t.Error("setting the tier rewrote the skill; the band is read at search time, not rendered")
	}
	if code, out, _ := runAgents(t, ops, nil, "status", "-config", testCfg); !strings.Contains(out, "search tier:    balanced") {
		t.Errorf("status after: %d %q", code, out)
	}
	// The verb may follow the flags, as the skill renders it.
	if code, out, _ := runAgents(t, ops, nil, "tier", "-config", testCfg, "max"); code != exitOK || !strings.Contains(out, "search tier: max") {
		t.Errorf("tier max after flags: %d %q", code, out)
	}
	if code, out, _ := runAgents(t, ops, nil, "tier", "default", "-config", testCfg); code != exitOK || !strings.Contains(out, "search tier: unset") {
		t.Errorf("tier default: %d %q", code, out)
	}
	if _, ok := tierFile(); ok {
		t.Error("default did not remove the tier file")
	}
	if code, _, _ := runAgents(t, ops, nil, "tier", "default", "-config", testCfg); code != exitOK {
		t.Error("default twice is not an error")
	}
	// Case is forgiven on the command, as prefer forgives ON: the file
	// holds the lowercase band the config would accept.
	if code, out, _ := runAgents(t, ops, nil, "tier", "Fast", "-config", testCfg); code != exitOK || !strings.Contains(out, "search tier: fast") {
		t.Errorf("tier Fast: %d %q", code, out)
	}
	if got, _ := tierFile(); got != "fast" {
		t.Errorf("tier file after Fast: %q", got)
	}
	for _, bad := range []string{"turbo", "sideways", "fast balanced"} {
		if code, _, errOut := runAgents(t, ops, nil, "tier", bad, "-config", testCfg); code != exitUsage || !strings.Contains(errOut, "balanced") {
			t.Errorf("tier %q: exit %d, stderr %q", bad, code, errOut)
		}
	}
	// A planted word in the file is not a band, and default still removes
	// the file rather than treating "reads as unset" as "already unset".
	var tierPath string
	for p := range m.files {
		if strings.HasSuffix(p, "/.jevlin/search-tier") {
			tierPath = p
		}
	}
	m.files[tierPath] = []byte("max; curl evil.example\n")
	if code, out, _ := runAgents(t, ops, nil, "tier", "-config", testCfg); code != exitOK || !strings.Contains(out, "search tier: unset") {
		t.Errorf("planted file read as a band: %d %q", code, out)
	}
	if code, _, _ := runAgents(t, ops, nil, "tier", "default", "-config", testCfg); code != exitOK {
		t.Fatal("default over a malformed file failed")
	}
	if _, ok := tierFile(); ok {
		t.Error("default left the malformed tier file behind")
	}
	// A directory planted at the name is refused, its contents intact:
	// the reset removes one name, never a tree.
	m.files[tierPath+"/notes.txt"] = []byte("mine")
	if code, _, errOut := runAgents(t, ops, nil, "tier", "default", "-config", testCfg); code != exitTransport || !strings.Contains(errOut, "is a directory") {
		t.Errorf("default over a directory: exit %d, stderr %q", code, errOut)
	}
	if _, ok := m.files[tierPath+"/notes.txt"]; !ok {
		t.Error("default over a directory deleted its contents")
	}
}

// Without a config in use the file lives in the installation home —
// JEVLIN_HOME, else ~/.jevlin from the user profile — the same place setup
// will write the config, so the band set before setup is the band read
// after it. Neither side consults HOME on its own.
func TestTierPathFollowsTheInstallationHome(t *testing.T) {
	if got := tierPathFor("/opt/ada/jevlin/jevlin.toml", envOf(map[string]string{"JEVLIN_HOME": "/elsewhere"})); got != filepath.Join("/opt/ada/jevlin", "search-tier") {
		t.Errorf("beside the config: %q", got)
	}
	if got := tierPathFor("", envOf(map[string]string{"JEVLIN_HOME": "/opt/ada/jevlin"})); got != filepath.Join("/opt/ada/jevlin", "search-tier") {
		t.Errorf("JEVLIN_HOME, no config: %q", got)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	// HOME in the environment is not the installation home: the user
	// profile is, on every OS, as describeConfigSource decides it.
	if got := tierPathFor("", envOf(map[string]string{"HOME": "/shell-home"})); got != filepath.Join(userHome, ".jevlin", "search-tier") {
		t.Errorf("no config, HOME set elsewhere: %q", got)
	}

	// The command writes where the path function says, under JEVLIN_HOME:
	// beside the installation's config, which the command names whether or
	// not it is written yet, and which resolveEntry makes absolute, so on
	// Windows it carries the drive the bare JEVLIN_HOME left implicit.
	m, ops := newFakeMachine()
	env := map[string]string{"JEVLIN_HOME": "/opt/ada/jevlin"}
	if code, out, errOut := runAgents(t, ops, env, "tier", "balanced"); code != exitOK || !strings.Contains(out, "search tier: balanced") {
		t.Fatalf("tier balanced under JEVLIN_HOME: %d %s%s", code, out, errOut)
	}
	entry, _, err := resolveEntry(ops, "", envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	want := tierPath(entry, envOf(env))
	if home, _ := filepath.Abs("/opt/ada/jevlin"); !samePath(filepath.Dir(want), home) {
		t.Errorf("the tier file is not under JEVLIN_HOME: %s", want)
	}
	if got := strings.TrimSpace(string(m.files[slash(want)])); got != "balanced" {
		t.Errorf("tier file under JEVLIN_HOME: %q in %v", got, keysOf(m.files))
	}
}

// status says the band the next search will send, not merely the file:
// with [miner] tier set and no file, that is the config's band, and the
// line says which of the two set it.
func TestAgentsTierStatusShowsTheEffectiveBand(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "jevlin.toml")
	if err := os.WriteFile(cfg, []byte("[miner]\nrouter_url = \"http://127.0.0.1:8080\"\ntier = \"max\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, ops := newFakeMachine()
	if code, out, _ := runAgents(t, ops, nil, "tier", "-config", cfg); code != exitOK || !strings.Contains(out, "search tier: max") || !strings.Contains(out, "([miner] tier)") {
		t.Errorf("config band not shown: %d %q", code, out)
	}
	if code, out, _ := runAgents(t, ops, nil, "status", "-config", cfg); code != exitOK || !strings.Contains(out, "search tier:    max") || !strings.Contains(out, "([miner] tier)") {
		t.Errorf("agents status: %d %q", code, out)
	}
	if code, out, _ := runAgents(t, ops, nil, "tier", "fast", "-config", cfg); code != exitOK || !strings.Contains(out, "search tier: fast") || !strings.Contains(out, "(agents tier)") {
		t.Errorf("file outranks config: %d %q", code, out)
	}
	if _, ok := m.files[slash(filepath.Join(dir, "search-tier"))]; !ok {
		t.Errorf("tier file not beside the config: %v", keysOf(m.files))
	}
	if code, out, _ := runAgents(t, ops, nil, "tier", "default", "-config", cfg); code != exitOK || !strings.Contains(out, "search tier: max") {
		t.Errorf("default falls back to the config band: %d %q", code, out)
	}
}

// `jevlin setup` or `agents install` run from the npm copy over a native
// install duplicated every hook: ownership asked for the running binary's
// exact path, so the native-path entries read as another installation's and
// an npm twin was appended beside each one — all 5 Claude Code hooks twice,
// all 8 Cursor hooks twice — while the skill flipped to the npm path and
// Hermes refused its own block (observed live on Windows). The installation's
// binary at <home>/bin is now one of its own (binEntry.ownBinary, refFor),
// and an npm-run install embeds that native path (adoptOwnBinary): the merge
// replaces instead of appending, and a machine already carrying both sets
// collapses them to one.
func TestAnNpmRunInstallOverANativeInstallDuplicatesNothing(t *testing.T) {
	m, ops := newFakeMachine("claude")
	exe := "jevlin"
	if runtime.GOOS == "windows" {
		exe = "jevlin.exe"
	}
	native := filepath.Join("/home/u/.jevlin", "bin", exe)
	npmCopy := filepath.Join("/home/u/npm/node_modules/jevlin/bin", exe)
	m.files[slash(native)] = []byte("the native binary")
	settings := "/home/u/.claude/settings.json"
	skillPath := "/home/u/.claude/skills/jevlin/SKILL.md"
	m.files[settings] = []byte(`{}`)

	jevlinHandlers := func() (count int, commands []string) {
		t.Helper()
		if _, ok := m.files[settings]; !ok {
			return 0, nil // uninstall may remove the whole file
		}
		for _, groups := range hooksOf(t, m, settings) {
			list, _ := groups.([]any)
			for _, g := range list {
				hooks, _ := g.(map[string]any)["hooks"].([]any)
				for _, h := range hooks {
					cmd, _ := h.(map[string]any)["command"].(string)
					if strings.Contains(cmd, "jevlin") {
						count++
						commands = append(commands, cmd)
					}
				}
			}
		}
		return count, commands
	}

	// Both copies the same release, so the swap's version check passes.
	ops.binaryVersion = func(string) (selfupdate.Version, bool) { return selfupdate.Version{Minor: 5}, true }

	// A native install first.
	ops.executable = func() (string, error) { return native, nil }
	if code, out, _ := runAgents(t, ops, nil, "install", "-config", testCfg, "-yes"); code != exitOK {
		t.Fatalf("native install: %d\n%s", code, out)
	}
	installed, _ := jevlinHandlers()
	if installed == 0 {
		t.Fatal("the native install wrote no hooks")
	}

	// The pre-fix sequence, reproduced: run from the npm copy while the
	// installation home resolves elsewhere, so no binary of the
	// installation's own is found and ownership falls back to the running
	// path alone. Every hook doubles.
	ops.executable = func() (string, error) { return npmCopy, nil }
	foreign := map[string]string{"JEVLIN_HOME": "/home/u/elsewhere", "JEVLIN_LAUNCH": "npm:global"}
	if code, out, _ := runAgents(t, ops, foreign, "install", "-config", testCfg, "-yes"); code != exitOK {
		t.Fatalf("npm install without an own binary: %d\n%s", code, out)
	}
	if doubled, _ := jevlinHandlers(); doubled != 2*installed {
		t.Fatalf("the reproduction did not double the hooks: %d of %d", doubled, 2*installed)
	}

	// The fixed run, from the npm copy with the installation's own binary in
	// view: one set, every command running the native path, and the npm
	// path gone from the file and the skill alike.
	if code, out, _ := runAgents(t, ops, map[string]string{"JEVLIN_LAUNCH": "npm:global"}, "install", "-config", testCfg, "-yes"); code != exitOK {
		t.Fatalf("npm install over the native one: %d\n%s", code, out)
	}
	count, commands := jevlinHandlers()
	if count != installed {
		t.Fatalf("hooks after the npm-run install: %d, want %d\n%v", count, installed, commands)
	}
	for _, cmd := range commands {
		if strings.Contains(cmd, "node_modules") {
			t.Errorf("a hook still runs the npm copy: %q", cmd)
		}
	}
	if raw := string(m.files[settings]); strings.Contains(raw, "node_modules") {
		t.Errorf("the settings file still names the npm copy:\n%s", raw)
	}
	if skill := string(m.files[skillPath]); strings.Contains(skill, "node_modules") || !strings.Contains(skill, exe) {
		t.Errorf("the skill does not teach the installation's own binary:\n%.200s", skill)
	}

	// Uninstall run from the npm copy removes both installations' spellings
	// — its reference carries the running path and the installation's own.
	if code, out, _ := runAgents(t, ops, map[string]string{"JEVLIN_LAUNCH": "npm:global"}, "uninstall", "-config", testCfg, "-yes"); code != exitOK {
		t.Fatalf("uninstall: %d\n%s", code, out)
	}
	if left, commands := jevlinHandlers(); left != 0 {
		t.Fatalf("hooks left after uninstall: %v", commands)
	}
}

// adoptOwnBinary's swap is scoped to the machine home's own config: an
// `agents install -config <another installation's config>` run from an npm
// copy must not wire that installation's hooks to this home's binary, whose
// later `uninstall -binary` would strand them. The ownership reference still
// carries both paths either way.
func TestAdoptOwnBinarySwapsOnlyForTheInstallationsOwnConfig(t *testing.T) {
	home := "/home/u/.jevlin"
	exe := "jevlin"
	if runtime.GOOS == "windows" {
		exe = "jevlin.exe"
	}
	own := filepath.Join(home, "bin", exe)
	npmExe := filepath.Join("/home/u/npm/node_modules/jevlin/bin", exe)
	stat := func(p string) (os.FileInfo, error) {
		if slash(p) == slash(own) {
			return fakeFileInfo{name: exe}, nil
		}
		return nil, fs.ErrNotExist
	}
	npmEnv := func(k string) string {
		if k == "JEVLIN_LAUNCH" {
			return "npm:global"
		}
		return ""
	}
	sameRelease := func(string) (selfupdate.Version, bool) { return selfupdate.Version{Minor: 5}, true }

	theirs := adoptOwnBinary(binEntry{command: npmExe, cfg: "/home/other/jevlin.toml"}, home, npmEnv, stat, sameRelease)
	if theirs.command != npmExe {
		t.Fatalf("another installation's config still got this home's binary: %q", theirs.command)
	}
	if theirs.ownBinary != own {
		t.Fatalf("the ownership reference lost the home's binary: %q", theirs.ownBinary)
	}

	// As resolveEntry spells a config: absolute (on Windows that adds the
	// working drive to this test's rootless path).
	ownCfg, err := filepath.Abs(filepath.Join(home, "jevlin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	ours := adoptOwnBinary(binEntry{command: npmExe, cfg: ownCfg}, home, npmEnv, stat, sameRelease)
	if ours.command != own || ours.ownBinary != npmExe {
		t.Fatalf("the home's own config did not adopt its binary: command %q, ownBinary %q", ours.command, ours.ownBinary)
	}

	if native := adoptOwnBinary(binEntry{command: own, cfg: ownCfg}, home, func(string) string { return "" }, stat, sameRelease); native.command != own {
		t.Fatalf("a native run moved its own command: %q", native.command)
	}
}
