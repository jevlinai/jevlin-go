package main

import (
	"bytes"
	"io"
	"unicode/utf8"

	"github.com/jevlinai/jevlin-go/internal/termtext"
)

// terminalSafeWriter passes a command's own text through and replaces every
// character internal/termtext refuses, and every byte that is not UTF-8,
// with U+FFFD, except the newline and tab the command's own lines use. It is
// for a command whose messages carry text read from a directory a sandboxed
// command can write: a reader who sees U+FFFD knows something was removed,
// and no message has to remember which of its parts came from where.
type terminalSafeWriter struct {
	w io.Writer
}

func (t terminalSafeWriter) Write(p []byte) (int, error) {
	var b bytes.Buffer
	b.Grow(len(p))
	for rest := p; len(rest) > 0; {
		r, n := utf8.DecodeRune(rest)
		switch {
		case r == '\n' || r == '\t':
			b.WriteByte(byte(r))
		case r == utf8.RuneError && n <= 1, termtext.HasControlChar(string(r)):
			b.WriteRune(utf8.RuneError)
		default:
			b.Write(rest[:n])
		}
		rest = rest[n:]
	}
	if _, err := t.w.Write(b.Bytes()); err != nil {
		return 0, err
	}
	return len(p), nil
}
