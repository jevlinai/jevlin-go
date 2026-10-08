package main

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// The writer keeps a command's own lines and removes what a planted name or
// id would bring with it, invalid bytes included: 0x9b alone is CSI to an
// 8-bit terminal.
func TestTerminalSafeWriterRemovesWhatATerminalActsOn(t *testing.T) {
	var b bytes.Buffer
	in := "jevlin flush: spool: x\x1b]52;c;ZXZpbA==\a \u009b2J \u202e \U000e0049 \u200b \u2028 \x9b José\tok\n"
	n, err := terminalSafeWriter{&b}.Write([]byte(in))
	if err != nil || n != len(in) {
		t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(in))
	}
	out := b.String()
	// Each planted character named, never asked of termtext: an oracle that
	// is the rule under test stays green when the rule is narrowed.
	for _, r := range []rune{0x1b, 0x07, 0x9b, 0x202e, 0xe0049, 0x200b, 0x2028} {
		if strings.ContainsRune(out, r) {
			t.Errorf("U+%04X came through: %q", r, out)
		}
	}
	if bytes.IndexByte(b.Bytes(), 0x9b) >= 0 || !utf8.Valid(b.Bytes()) {
		t.Errorf("an invalid byte came through: %q", out)
	}
	for _, want := range []string{"jevlin flush: spool: x", "José\tok\n"} {
		if !bytes.Contains(b.Bytes(), []byte(want)) {
			t.Errorf("the command's own text %q was changed: %q", want, out)
		}
	}
}
