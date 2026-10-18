//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Everything a foreground flush prints passes terminalSafeWriter, so no
// message has to remember which of its parts came from a file a sandboxed
// command can name. The writer is tested alone, and the structural guard's
// flush leg sees only a spool id that is also quoted at its source, so each
// masked the other: removing the wiring left every test green. A config
// path is the one string a test hands a foreground flush that it prints
// unquoted, so the wiring is held through one. POSIX only: Windows refuses
// these characters in a file name.
func TestEverythingAForegroundFlushPrintsPassesTheTerminalSafeWriter(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "x\x1b]52;c;ZXZpbA==\a\u202e\U000e0049.toml")
	if err := os.WriteFile(cfg, []byte("this is = not [valid toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	_ = cmdFlush([]string{"-config", cfg, "-force"}, &stdout, &stderr, noEnv)
	out := stdout.String() + stderr.String()
	if !strings.Contains(out, "jevlin flush") {
		t.Fatalf("flush said nothing about the config, so this proves nothing: %q", out)
	}
	for _, r := range []rune{0x1b, 0x07, 0x202e, 0xe0049} {
		if strings.ContainsRune(out, r) {
			t.Fatalf("U+%04X reached the terminal from flush: %q", r, out)
		}
	}
}
