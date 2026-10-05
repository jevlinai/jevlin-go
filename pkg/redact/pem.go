package redact

import "strings"

// A PEM private key, quoted or not, assigned or not: TraceText removes
// the whole block, markers included, before any other step, so no later
// step can change the markers it reads. The quoted-value line cap
// (quotedValueMaxLines) no longer decides whether a key's body goes: an
// encrypted 8192-bit key runs to about a hundred lines, and a key pasted
// on its own has no assignment at all. cmd/jevlin/agent_trace_common.js
// implements the same rule.

const (
	pemBegin = "-----BEGIN "
	pemEnd   = "-----END "
	pemDash  = "-----"
)

// pemMarkerEnd reads the label after a marker's opening word at i (just
// past "-----BEGIN " or "-----END ") and returns the index just past the
// marker's closing dashes and whether the label names a private key:
// PRIVATE KEY, RSA PRIVATE KEY, ENCRYPTED PRIVATE KEY, OPENSSH PRIVATE KEY,
// PGP PRIVATE KEY BLOCK and the like. -1 when no marker closes there.
func pemMarkerEnd(s string, i int) (int, bool) {
	j := i
	for j < len(s) && j-i < 64 && (isUpper(s[j]) || isDigit(s[j]) || s[j] == ' ') {
		j++
	}
	if !strings.HasPrefix(s[j:], pemDash) {
		return -1, false
	}
	label := s[i:j]
	return j + len(pemDash), strings.HasSuffix(label, "PRIVATE KEY") || strings.HasSuffix(label, "PRIVATE KEY BLOCK")
}

// redactPrivateKeyBlocks replaces each private key block with the
// placeholder: from its BEGIN marker to the next END marker for a private
// key, when no other BEGIN marker comes first. A block with no such END is
// cut short or was pasted in part, and goes from its BEGIN marker to the
// end of that line and on through the lines that follow it while they are
// a body's: base64 only, an RFC 1421 header (Proc-Type:, DEK-Info:), or
// the empty line after one.
//
// The cost: prose that quotes a BEGIN marker and later an END marker loses
// what stands between them.
func redactPrivateKeyBlocks(s string) string {
	i := strings.Index(s, pemBegin)
	if i < 0 {
		return s
	}
	var b strings.Builder
	last, changed := 0, false
	// The first END marker at or after the last search, kept so that many
	// BEGIN markers with none do not each search the rest of the text.
	nextEnd, searched := -1, false
	for i >= 0 {
		next := i + len(pemBegin)
		if h, private := pemMarkerEnd(s, next); h >= 0 && private {
			if !searched || (nextEnd >= 0 && nextEnd < h) {
				nextEnd, searched = indexFrom(s, pemEnd, h), true
			}
			nextBegin := indexFrom(s, pemBegin, h)
			end := -1
			if nextEnd >= 0 && (nextBegin < 0 || nextEnd < nextBegin) {
				if e, endPrivate := pemMarkerEnd(s, nextEnd+len(pemEnd)); e >= 0 && endPrivate {
					end = e
				}
			}
			if end < 0 {
				end = pemBodyEnd(s, h)
			}
			b.WriteString(s[last:i])
			b.WriteString(placeholder)
			last, next, changed = end, end, true
		}
		i = indexFrom(s, pemBegin, next)
	}
	if !changed {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// indexFrom is strings.Index from i, as an index into s, or -1.
func indexFrom(s, sub string, i int) int {
	if i > len(s) {
		return -1
	}
	j := strings.Index(s[i:], sub)
	if j < 0 {
		return -1
	}
	return i + j
}

// pemBodyEnd is where an unclosed block that starts its header line's
// remainder at h ends: that line's end, then each following line that a
// PEM body holds, with the empty line after a header.
func pemBodyEnd(s string, h int) int {
	end := lineEnd(s, h)
	afterHeader := false
	for end < len(s) {
		start := end
		if s[start] == '\r' {
			start++
		}
		if start >= len(s) || s[start] != '\n' {
			break
		}
		start++
		line := s[start:lineEnd(s, start)]
		switch {
		case line == "" && afterHeader:
			afterHeader = false
		case strings.HasPrefix(line, "Proc-Type:") || strings.HasPrefix(line, "DEK-Info:"):
			afterHeader = true
		case line != "" && isBase64Line(line):
			afterHeader = false
		default:
			return end
		}
		end = start + len(line)
	}
	return end
}

func isBase64Line(line string) bool {
	for i := 0; i < len(line); i++ {
		c := line[i]
		if !isUpper(c) && !isLower(c) && !isDigit(c) && c != '+' && c != '/' && c != '=' {
			return false
		}
	}
	return true
}
