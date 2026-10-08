package main

import (
	"bytes"
	"testing"
	"unicode/utf8"

	"github.com/jevlinai/jevlin-go/internal/termtext"
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
	for _, r := range out {
		if r != '\n' && r != '\t' && termtext.HasControlChar(string(r)) {
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
