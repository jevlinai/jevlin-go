package redact

import "strings"

// A payment card number, which TraceText removes and the log path does not:
// a log line is this client's own words and never holds one, while a long
// number in it (a request id, a size) is what its reader needs.
//
// A number is a card's when all of these hold:
//
//   - its digits are written as one run of 13 to 19, or grouped by single
//     spaces or dashes as cards are printed: four groups of four, with a
//     last group of one to three digits or none (16 to 19 digits), or
//     4-6-5 and 4-6-4 (15 and 14, American Express and Diners Club);
//   - its first digit is 3, 4, 5 or 6, or its first four digits are 2200
//     to 2720 (Mir and Mastercard's 2-series), the major industry
//     identifiers of ISO/IEC 7812 that card networks issue under;
//   - its digits pass the Luhn check, as every card number's do;
//   - it is not part of a word or another number: nothing joined to it
//     that a word or an identifier has (a letter, a digit, `_`), no `.` or
//     `+` before it (a decimal, a version, a phone number in E.164), no
//     `.` or `-` on either side unless a blank or the end of the text is on
//     its other side (a decimal, a file name, a hyphenated identifier), no
//     `]` before it and no `@` after it (a placeholder, an address).
//
// What a trace carries that is shaped like this, and why it stays: a commit
// SHA is hex, and a run of its digits is joined to its letters; a Unix time
// in seconds has 10 digits, and in milliseconds, microseconds or
// nanoseconds it starts with 1 until the year 2286, as do the snowflake ids
// of Discord and X at present; a compact date (20261113101500) starts with
// 20, below 2200; a version or a decimal is joined by dots; a phone number
// has at most 15 digits, starts with `+` when it has that many, and is
// grouped in ways no card is (+44 20 7946 0958); a list of small numbers
// (1 2 3 4 5 6 7 8 9 10 11 12 13), a row of a matrix or a hex dump of
// zeros is not grouped in fours, or starts with 0 or 1. The cost, stated:
// a number with none of those marks that happens to pass the check goes, one
// in ten of them: a long decimal id in JSON ({"id": 4012345678901234}), a
// hash printed as a decimal, a hex dump whose groups of four are all
// digits, and an IMEI, which uses the same check (and identifies a device).
// A run with no separators is the riskier form, and is the reason for the
// prefix and the boundary rules; restricting it further would miss a card
// pasted from a form, which is how a model most often meets one.
//
// cmd/jevlin/agent_trace_common.js implements the same scan
// (redactTraceCardNumbers).

const (
	cardMinDigits = 13
	cardMaxDigits = 19
)

// redactCardNumbers replaces every card number in s. It reads the text as
// sequences of digit groups, each group a maximal run of digits and each
// joined to the next by one space or one dash, and tries a card at each
// group of a sequence in turn, taking the first that holds and going on
// after it. Every try reads at most five groups, so the scan is linear.
func redactCardNumbers(s string) string {
	var b strings.Builder
	var groups [][2]int
	last, changed := 0, false
	for i := 0; i < len(s); {
		if !isDigit(s[i]) {
			i++
			continue
		}
		groups = groups[:0]
		j := i
		for {
			k := j
			for k < len(s) && isDigit(s[k]) {
				k++
			}
			groups = append(groups, [2]int{j, k})
			if k+1 < len(s) && (s[k] == ' ' || s[k] == '-') && isDigit(s[k+1]) {
				j = k + 1
				continue
			}
			break
		}
		for g := 0; g < len(groups); {
			n := cardAt(s, groups, g)
			if n == 0 {
				g++
				continue
			}
			b.WriteString(s[last:groups[g][0]])
			b.WriteString(placeholder)
			last, changed = groups[g+n-1][1], true
			g += n
		}
		i = groups[len(groups)-1][1]
	}
	if !changed {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// cardAt is how many groups, from groups[g], make a card number, or 0.
func cardAt(s string, groups [][2]int, g int) int {
	if g == 0 && !cardStartsFree(s, groups[0][0]) {
		return 0
	}
	length := func(k int) int {
		if k >= len(groups) {
			return 0
		}
		return groups[k][1] - groups[k][0]
	}
	try := func(n int) bool {
		end := g + n - 1
		if end >= len(groups) || (end == len(groups)-1 && !cardEndsFree(s, groups[end][1])) {
			return false
		}
		return cardDigitsValid(s, groups[g:end+1])
	}
	switch first := length(g); {
	case first >= cardMinDigits && first <= cardMaxDigits:
		if try(1) {
			return 1
		}
	case first == 4 && length(g+1) == 4 && length(g+2) == 4 && length(g+3) == 4:
		if tail := length(g + 4); tail >= 1 && tail <= 3 && try(5) {
			return 5
		}
		if try(4) {
			return 4
		}
	case first == 4 && length(g+1) == 6 && (length(g+2) == 5 || length(g+2) == 4):
		if try(3) {
			return 3
		}
	}
	return 0
}

// cardStartsFree reports whether a number starting at p is not the end of a
// word, a decimal, a phone number or a hyphenated identifier. s[p-1] is not
// a digit: p starts a sequence.
func cardStartsFree(s string, p int) bool {
	if p == 0 {
		return true
	}
	switch c := s[p-1]; {
	case isWordChar(c), c == '.', c == '+', c == ']':
		return false
	case c == '-':
		return p == 1 || isSpace(s[p-2])
	}
	return true
}

// cardEndsFree reports whether a number ending at p, the end of its
// sequence, is not the start of a word, a decimal, a file name, a
// hyphenated identifier or an address.
func cardEndsFree(s string, p int) bool {
	if p == len(s) {
		return true
	}
	switch c := s[p]; {
	case isWordChar(c), c == '@':
		return false
	case c == '.', c == '-':
		return p+1 == len(s) || isSpace(s[p+1])
	}
	return true
}

// cardDigitsValid reports whether the digits of groups, read as one number,
// start as a card network's do and pass the Luhn check.
func cardDigitsValid(s string, groups [][2]int) bool {
	var d [cardMaxDigits]byte
	n := 0
	for _, gr := range groups {
		for i := gr[0]; i < gr[1]; i++ {
			if n == len(d) {
				return false
			}
			d[n] = s[i] - '0'
			n++
		}
	}
	if n < cardMinDigits {
		return false
	}
	switch d[0] {
	case 3, 4, 5, 6:
	case 2:
		if p := int(d[0])*1000 + int(d[1])*100 + int(d[2])*10 + int(d[3]); p < 2200 || p > 2720 {
			return false
		}
	default:
		return false
	}
	sum := 0
	for i := 0; i < n; i++ {
		v := int(d[n-1-i])
		if i%2 == 1 {
			if v *= 2; v > 9 {
				v -= 9
			}
		}
		sum += v
	}
	return sum%10 == 0
}
