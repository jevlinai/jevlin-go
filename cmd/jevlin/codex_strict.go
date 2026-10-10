package main

// Every byte this client writes into Codex's config.toml must be a file
// Codex itself would load.
//
// The rest of this client decodes TOML with BurntSushi/toml, which is
// lenient in exactly the places this file's edits can go wrong: it accepts
// a [features.network_proxy] header after `features = { ... }` (extending an
// inline table) and a [permissions.work.filesystem] header after
// `filesystem."/x" = "write"` inside [permissions.work] (redefining a table
// made by dotted keys). TOML 1.0 forbids both, and so does Codex: it refuses
// such a file with "failed to load bootstrap configuration ... TOML parse
// error" and does not start. An install that decoded its own output with the
// lenient reader once wrote exactly that file and exited 0.
//
// So every planned write to Codex's config is parsed again with
// pelletier/go-toml/v2, which rejects what TOML 1.0 rejects. Its verdict was
// checked against Codex's own on every shape this client can produce
// (codex_strict_test.go, verdicts from `codex features list` on codex-cli
// 0.158.0); it agreed on all of them, and BurntSushi disagreed on four.
// Rejected: replacing BurntSushi everywhere, which would change how every
// config this client reads is decoded, for a check only Codex's file needs;
// and a hand-written structural check, which would be a TOML parser of our
// own to get wrong.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	strict "github.com/pelletier/go-toml/v2"
)

// codexTOMLError is why Codex's TOML parser would refuse text, or nil.
func codexTOMLError(text string) error {
	var v map[string]any
	if err := strict.Unmarshal([]byte(text), &v); err != nil {
		var de *strict.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return fmt.Errorf("line %d, column %d: %s", row, col, de.Error())
		}
		return err
	}
	return nil
}

// codexHeaderCollisions names the tables of ours that the participant's own
// file already defines inline or through dotted keys, so that a header for
// them would make a file Codex refuses. Each header is tried alone against
// the participant's text, which is what says which one collides.
func codexHeaderCollisions(participant string, headers []string) []string {
	var out []string
	for _, h := range headers {
		if codexTOMLError(participant) != nil {
			return nil // already refused; codexWriteRefusal says so
		}
		probe := strings.TrimRight(participant, "\n") + "\n\n[" + h + "]\n"
		if codexTOMLError(probe) != nil {
			out = append(out, "["+h+"]")
		}
	}
	return out
}

// codexWriteRefusal is why next must not be written over existing, or "".
// participant is the participant's own text, used only to name a colliding
// table in the sentence.
func codexWriteRefusal(existing, next []byte, participant string, headers []string) string {
	err := codexTOMLError(string(next))
	if err == nil {
		return ""
	}
	if before := codexTOMLError(string(existing)); before != nil {
		return fmt.Sprintf("Codex already refuses this file (%v), so nothing was written to it; fix it first", before)
	}
	if hit := codexHeaderCollisions(participant, headers); len(hit) > 0 {
		return fmt.Sprintf("your config defines %s inline or with dotted keys, and adding jevlin's table there would make a file Codex refuses (%v); nothing was written for Codex. Write that table as a [header] of its own and run this again",
			strings.Join(hit, " and "), err)
	}
	return fmt.Sprintf("the result would be a file Codex refuses (%v), so nothing was written to it", err)
}

// planCodexWrite is planWrite for Codex's config.toml, held to Codex's
// parser. It refuses rather than write a file Codex would not load.
func planCodexWrite(ops agentOps, label, path string, existing, next []byte, participant string, headers []string, mode os.FileMode, why string, p *agentPlan) (changed, refused bool) {
	if r := codexWriteRefusal(existing, next, participant, headers); r != "" {
		p.refused = append(p.refused, fmt.Sprintf("%s: %s: %s", label, path, r))
		return false, true
	}
	return planWrite(ops, label, path, next, mode, why, p), false
}
