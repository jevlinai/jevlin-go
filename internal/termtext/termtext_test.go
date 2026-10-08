package termtext

import "testing"

func TestHasControlCharNamesEveryRangeItRefuses(t *testing.T) {
	refused := []rune{
		0x00, 0x07, 0x09, 0x0a, 0x0d, 0x1b, 0x1f, // C0, BEL, tab, newline, CR, ESC
		0x7f,             // DEL
		0x80, 0x9b, 0x9f, // C1, one-byte CSI
		0x061c, 0x200e, 0x200f, // ALM, LRM, RLM
		0x202a, 0x202e, // embedding and override
		0x2066, 0x2069, // isolates
	}
	for _, r := range refused {
		if !HasControlChar("twilight1" + string(r) + "abc") {
			t.Errorf("U+%04X is not refused", r)
		}
	}
	allowed := []string{
		"", "twilight1lpdtlehaqn95mkcfgae8rut89s4pq9ayxdp4yc", "https://platform.example/claim/AB12-CD34",
		"José", "日本", "a b", "\u00a0",
	}
	for _, s := range allowed {
		if HasControlChar(s) {
			t.Errorf("%q is refused", s)
		}
	}
}
