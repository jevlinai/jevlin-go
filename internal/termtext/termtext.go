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

// HasControlChar reports any C0 control character (including \n, \r and
// \t), DEL, C1 control (U+0080-U+009F, which includes the one-byte CSI
// U+009B) or bidi formatting character. None of them belongs in a claim
// URL, a claim code, a scope, a slot name, a refusal message, a payout
// address or a health record's detail, and each can forge or reorder what
// a terminal shows. It is a test for a REFUSAL, not a sanitizer: a value
// holding one is not a shape this client trusts enough to guess what was
// meant.
func HasControlChar(s string) bool {
	for _, r := range s {
		switch {
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return true
		case r == 0x061c, r == 0x200e, r == 0x200f,
			r >= 0x202a && r <= 0x202e,
			r >= 0x2066 && r <= 0x2069:
			return true
		}
	}
	return false
}
