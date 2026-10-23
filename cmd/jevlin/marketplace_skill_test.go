package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// marketplaceSkillPath is where the marketplaces read the skill from:
// the repository, at the path skills.sh, Claude Code, Codex and Cursor
// all look in.
const marketplaceSkillPath = "skills/jevlin/SKILL.md"

// TestMarketplaceSkillIsTheTemplateRendered: the committed file is what
// marketplaceSkill renders, byte for byte. Regenerate it with
//
//	JEVLIN_UPDATE_MARKETPLACE_SKILL=1 go test ./cmd/jevlin -run TestMarketplaceSkillIsTheTemplateRendered
//
// after any change to skill.md, the renderer, or the marketplace
// preamble, and commit the result with the change.
func TestMarketplaceSkillIsTheTemplateRendered(t *testing.T) {
	want, err := marketplaceSkill()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(moduleRoot(t), marketplaceSkillPath)
	if os.Getenv("JEVLIN_UPDATE_MARKETPLACE_SKILL") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil { // #nosec G306 -- a committed source file, not a secret
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path) // #nosec G304 -- a fixed path under this module's own root
	if err != nil {
		t.Fatalf("read %s: %v", marketplaceSkillPath, err)
	}
	if bytes.Equal(got, want) {
		return
	}
	line, col, gotCtx, wantCtx := firstDifference(got, want)
	t.Fatalf("%s is not the rendered marketplace skill.\n"+
		"first difference at line %d, column %d:\n"+
		"  file:      %q\n"+
		"  rendered:  %q\n"+
		"Regenerate it: JEVLIN_UPDATE_MARKETPLACE_SKILL=1 go test ./cmd/jevlin -run TestMarketplaceSkillIsTheTemplateRendered",
		marketplaceSkillPath, line, col, gotCtx, wantCtx)
}

// TestMarketplaceSkillCarriesBothShellsAndThePreference: what makes this
// copy different from an installed one is present, and the PowerShell
// form is the renderer's, encoding line included.
func TestMarketplaceSkillCarriesBothShellsAndThePreference(t *testing.T) {
	out, err := marketplaceSkill()
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"If the tool you are calling runs PowerShell:",
		"$OutputEncoding = [System.Text.UTF8Encoding]::new($false)",
		"'@ | & 'jevlin' search --stdin",
		"'jevlin' search --stdin <<'JSON'",
		"'jevlin' agents prefer status",
		"npm install -g @jevlinsearch/jevlin && jevlin setup",
		marketplaceSkillRuleTo,
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("marketplace skill lacks %q", want)
		}
	}
	if bytes.Contains(out, []byte(marketplaceSkillRuleFrom)) || bytes.Contains(out, []byte(`\u003c`)) {
		t.Errorf("marketplace skill kept the unconditioned rule or a JSON escape:\n%s", s[:600])
	}
}
