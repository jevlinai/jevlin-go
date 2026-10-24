package main

// Every command agents install renders names a config, the installation's
// own before setup has written it, so every Claude Code allow rule ends with
// that -config; and what an earlier version rendered without one is
// replaced, not left beside.

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// Before setup has written the installation's config, a command naming its
// path reads as one naming none: built-in defaults.
func TestTheInstallationsUnwrittenConfigReadsAsDefaults(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, setupConfigFile)
	getenv := envOf(map[string]string{"JEVLIN_HOME": home})
	cfg, src, err := loadConfig(path, getenv)
	if err != nil || src != "" || cfg == nil {
		t.Fatalf("loadConfig(%s) = %v, %q, %v; want defaults", path, cfg != nil, src, err)
	}
	// Any other absent file is still the error it always was.
	if _, _, err := loadConfig(filepath.Join(home, "other.toml"), getenv); err == nil {
		t.Error("an absent config that is not the installation's loaded")
	}
	// And so is the installation's path when JEVLIN_CONFIG names another.
	named := envOf(map[string]string{"JEVLIN_HOME": home, "JEVLIN_CONFIG": filepath.Join(home, "elsewhere.toml")})
	if _, _, err := loadConfig(path, named); err == nil {
		t.Error("the default path loaded while JEVLIN_CONFIG names another config")
	}
}

func TestEveryAllowRuleNamesAConfigWhenNoneIsWrittenYet(t *testing.T) {
	m, ops := newFakeMachine("claude")
	env := map[string]string{"JEVLIN_HOME": "/home/u/.jevlin"}
	entry, _, err := resolveEntry(ops, "", envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(filepath.Join("/home/u/.jevlin", setupConfigFile))
	if entry.cfg != want || !entry.implicit {
		t.Fatalf("resolveEntry with no config: cfg %q implicit %v, want %q, true", entry.cfg, entry.implicit, want)
	}
	if code, out, errOut := runAgents(t, ops, env, "install", "-yes", "-client", "claude"); code != exitOK {
		t.Fatalf("install: exit %d\n%s\n%s", code, out, errOut)
	}
	allow := allowOf(t, m, claudeSettingsPath)
	if len(allow) == 0 {
		t.Fatal("no allow rules written")
	}
	for _, r := range allow {
		if !ruleIsOurs(r, refFor(entry)) {
			continue
		}
		// A search rule ends after its -config; a fetch rule after the
		// --stdin that follows it.
		sub, tail := " search -config ", ":*)"
		if strings.Contains(r, " fetch -config ") {
			sub, tail = " fetch -config ", " --stdin:*)"
		}
		if !strings.Contains(r, sub) || !strings.HasSuffix(r, tail) {
			t.Errorf("a rule does not end with the installation's -config: %s", r)
			continue
		}
		words := renderedWords(strings.TrimSuffix(strings.SplitN(r, sub, 2)[1], tail))
		if len(words) != 1 {
			t.Errorf("a rule's config is not one word: %s", r)
			continue
		}
		named := false
		for _, cand := range unquoteRenderedPath(words[0]) {
			named = named || samePath(cand, want)
		}
		if !named {
			t.Errorf("a rule names another config than %s: %s", want, r)
		}
	}
}

// Rules and hooks an earlier version wrote without a config are this
// installation's, and an install replaces them rather than adding beside.
func TestRulesAndHooksWithoutAConfigAreReplacedOnInstall(t *testing.T) {
	m, ops := newFakeMachine("claude")
	env := map[string]string{"JEVLIN_HOME": "/home/u/.jevlin"}
	claude, ok := targetByID(targetsByKind(targetHost), "claude")
	if !ok {
		t.Fatal("no claude target")
	}
	bin, _ := ops.executable()
	earlier := buildInstallPlan(ops, ops.paths(envOf(env)), []installTarget{claude}, binEntry{command: bin}, envOf(env))
	if failures := commitPlan(ops, &earlier, io.Discard, io.Discard); failures != 0 {
		t.Fatalf("seeding the earlier install: %d failure(s)", failures)
	}
	const mine = "Bash(git status:*)"
	seeded := allowOf(t, m, claudeSettingsPath)
	if len(seeded) == 0 {
		t.Fatal("the earlier install wrote no rules")
	}
	for _, r := range seeded {
		if strings.Contains(r, "-config") {
			t.Fatalf("the seed is meant to hold rules without a config: %s", r)
		}
	}
	hooksBefore := hookCommands(t, m)
	if len(hooksBefore) == 0 {
		t.Fatal("the earlier install wrote no hooks")
	}
	doc := map[string]any{}
	if err := json.Unmarshal(m.files[claudeSettingsPath], &doc); err != nil {
		t.Fatal(err)
	}
	allowSeed := []any{mine}
	for _, r := range seeded {
		allowSeed = append(allowSeed, r)
	}
	doc["permissions"].(map[string]any)["allow"] = allowSeed
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	m.files[claudeSettingsPath] = append(b, '\n')

	if code, out, errOut := runAgents(t, ops, env, "install", "-yes", "-client", "claude"); code != exitOK {
		t.Fatalf("install: exit %d\n%s\n%s", code, out, errOut)
	}
	entry, _, _ := resolveEntry(ops, "", envOf(env))
	allow := allowOf(t, m, claudeSettingsPath)
	want := append([]string{mine}, claudeAllowRules(entry)...)
	if !sameRuleSet(allow, want) {
		t.Errorf("allow rules after the install:\n got %q\nwant %q", allow, want)
	}
	for _, r := range seeded {
		if countString(allow, r) != 0 {
			t.Errorf("a rule without a config survived: %s", r)
		}
	}
	hooksAfter := hookCommands(t, m)
	if len(hooksAfter) != len(hooksBefore) {
		t.Errorf("hook commands %d before, %d after: an earlier one was kept beside its replacement\n%q", len(hooksBefore), len(hooksAfter), hooksAfter)
	}
	for _, c := range hooksAfter {
		if !strings.Contains(c, "-config") {
			t.Errorf("a hook command without a config survived: %s", c)
		}
	}
}

// hookCommands is every hook command in Claude Code's settings.
func hookCommands(t *testing.T, m *fakeMachine) []string {
	t.Helper()
	var out []string
	for _, groups := range hooksOf(t, m, claudeSettingsPath) {
		for _, g := range groups.([]any) {
			for _, h := range g.(map[string]any)["hooks"].([]any) {
				out = append(out, h.(map[string]any)["command"].(string))
			}
		}
	}
	return out
}
