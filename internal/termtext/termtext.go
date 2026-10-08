// Package termtext holds the one rule for text this client received from
// somewhere else and will print: the platform's responses, and the records
// it wrote back into a state directory a sandboxed agent can rewrite.
//
// It is a package of its own because the rule has two readers that cannot
// import each other: pkg/platform already imports pkg/auth, so pkg/auth
// cannot reach pkg/platform's copy. A second copy of a character-range rule
// is how one of them stops covering a range the other does, so both read
// this one. cmd/jevlin's sanitizeForTerminal (render.go) is a different,
// deliberately broader policy that replaces rather than refuses, for search
// results, and is not this rule.
package termtext

import "unicode"

// HasControlChar reports any character a terminal acts on or a reader cannot
// see: every control character (Unicode category Cc: C0 including \n, \r and
// \t, DEL, and C1 including the one-byte CSI U+009B), every format character
// (Cf: the bidi embeddings, overrides, isolates and marks, the zero-width
// space and joiners, the byte-order mark and the tag characters U+E0000 to
// U+E007F, which spell text no terminal shows but a model reading the output
// does), and the line and paragraph separators (Zl, Zp: U+2028 and U+2029,
// which some renderers break a line at). None of them belongs in a claim URL,
// a claim code, an agent id, a scope, a slot name, a refusal message, a
// payout address or a health record's detail, and each can forge, reorder or
// hide what a terminal shows. Letters, marks and the no-break space are
// left alone: a participant's name is "José", a path can be in Japanese.
//
// It is a test for a REFUSAL, not a sanitizer: a value holding one is not a
// shape this client trusts enough to guess what was meant.
func HasControlChar(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return true
		}
	}
	return false
}
