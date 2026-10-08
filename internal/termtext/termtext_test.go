package termtext

import "testing"

func TestHasControlCharNamesEveryRangeItRefuses(t *testing.T) {
	refused := []rune{
		0x00, 0x07, 0x09, 0x0a, 0x0d, 0x1b, 0x1f, // C0, BEL, tab, newline, CR, ESC
		0x7f,             // DEL
		0x80, 0x9b, 0x9f, // C1, one-byte CSI
		0x061c, 0x200e, 0x200f, // ALM, LRM, RLM
		0x202a, 0x202b, 0x202c, 0x202d, 0x202e, // embeddings, pop, overrides: every one, not the ends
		0x2066, 0x2067, 0x2068, 0x2069, // isolates: every one
		0x200b, 0x200c, 0x200d, 0x2060, // zero-width space, non-joiner, joiner, word joiner
		0xfeff,                    // byte-order mark
		0xe0001, 0xe0049, 0xe007f, // tag characters: invisible text a model still reads
		0x2028, 0x2029, // line and paragraph separators
		0x00ad, // soft hyphen
	}
	for _, r := range refused {
		if !HasControlChar("twilight1" + string(r) + "abc") {
			t.Errorf("U+%04X is not refused", r)
		}
	}
	allowed := []string{
		"", "twilight1lpdtlehaqn95mkcfgae8rut89s4pq9ayxdp4yc", "https://platform.example/claim/AB12-CD34",
		"José", "日本", "a b", "\u00a0", "e\u0301", // a combining accent is a letter's, not a format character
		"2026-09-16T00:00:00Z", "agent-01a11680-4c2e",
	}
	for _, s := range allowed {
		if HasControlChar(s) {
			t.Errorf("%q is refused", s)
		}
	}
}
