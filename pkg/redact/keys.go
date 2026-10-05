package redact

import (
	"regexp"
	"strings"
)

// Two more ways a secret's name gives its value away, which TraceText
// applies after the assignment rule (assign.go) and the log path does not:
// a key followed by a colon, as YAML and JSON write it, and a long flag
// followed by its value as the next word. TraceText runs the flag rule,
// then the key rule, then the assignment rule: each reads context around a
// name (what follows `--`; a line's start or a quoted key; a `set`, a word
// that opens with a quote), and a removal by a later rule must not change
// what an earlier one read, or a second pass would read it differently. The
// differential run found both: a `set` removed with a flag's value, and a
// word's opening quote removed with a key's. Both read
// names with secretName. Each is narrower than the assignment rule because
// a colon and a following word are ordinary prose ("the password: use the
// vault", "pass --token to the command") in a way `NAME=` is not.
// cmd/jevlin/agent_trace_common.js implements the same rules.

// redactSecretKeys replaces the value of every secret key written with a
// colon:
//
//   - a key at the start of a line (after indentation and an optional `-`
//     list marker), quoted or not, followed by a colon and a blank or a
//     quote: the value is the rest of the line, with trailing blanks and
//     what trimValueTail hands back left outside. YAML's password: x y z,
//     and a pretty-printed JSON or JS object's "password": "x",.
//   - a key anywhere else, quoted or not, where a member of an object, a
//     map or an argument list starts (opensMember), followed by a colon and
//     a quoted value that closes on its own line: the value is the quoted
//     string (quotedStringEnd). Inline JSON {"client_secret": "x"} and a JS
//     object {password: 'x'}.
//
// Neither takes a value that is a reference, a placeholder or a type name
// (notASecret). Rejected: skipping a line-start value that ends as a line of
// code does, with `,`, `;` or `{`: a later step can remove that last
// character (an assignment, a home path's segment), and a second pass would
// then remove what the first kept.
//
// The cost, stated: a line that begins "Password: use the vault" loses the
// rest of the line, and "the password: hunter2" in the middle of a
// sentence keeps its value. An unquoted value after a key mid-line is the
// form prose takes, and a quoted one, or a key at a line's start, is the
// form config takes.
func redactSecretKeys(s string) string {
	c := strings.IndexByte(s, ':')
	if c < 0 {
		return s
	}
	var b strings.Builder
	last, changed := 0, false
	for c >= 0 {
		next := c + 1
		if start, end, ok := secretKeyValueAt(s, last, c); ok {
			b.WriteString(s[last:start])
			b.WriteString(placeholder)
			last, next, changed = end, end, true
		}
		i := strings.IndexByte(s[next:], ':')
		if i < 0 {
			break
		}
		c = next + i
	}
	if !changed {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// secretKeyValueAt reads the key in front of the colon at c, never reaching
// back past floor, and returns where a secret value starts and ends.
func secretKeyValueAt(s string, floor, c int) (start, end int, ok bool) {
	k := c
	for k > floor && isBlank(s[k-1]) {
		k--
	}
	var name string
	var keyStart int
	if k > floor && (s[k-1] == '"' || s[k-1] == '\'') {
		q := s[k-1]
		r := k - 1
		for r > floor && isNameChar(s[r-1]) {
			r--
		}
		if r == k-1 || r == floor || s[r-1] != q || !isNameStart(s[r]) || !isWordChar(s[k-2]) {
			return 0, 0, false
		}
		name, keyStart = s[r:k-1], r-1
	} else {
		if k < c {
			return 0, 0, false
		}
		r := k
		for r > floor && isNameChar(s[r-1]) {
			r--
		}
		if r == k || !isWordChar(s[k-1]) {
			return 0, 0, false
		}
		p := -1
		for q := r; q < k; q++ {
			if isNameStart(s[q]) && (q == 0 || !isWordChar(s[q-1])) {
				p = q
				break
			}
		}
		if p < 0 {
			return 0, 0, false
		}
		name, keyStart = s[p:k], p
	}
	if !secretName(name) {
		return 0, 0, false
	}
	v := c + 1
	for v < len(s) && isBlank(s[v]) {
		v++
	}
	if v >= len(s) || s[v] == '\n' || s[v] == '\r' {
		return 0, 0, false
	}
	quoted := s[v] == '"' || s[v] == '\''
	switch {
	case startsLine(s, floor, keyStart) && (v > c+1 || quoted):
		end = lineEnd(s, v)
		for end > v && isBlank(s[end-1]) {
			end--
		}
		end = trimValueTail(s, v, end)
	case quoted && opensMember(s, floor, keyStart):
		if end = quotedStringEnd(s, v); end < 0 {
			return 0, 0, false
		}
	default:
		return 0, 0, false
	}
	if end == v || s[v:end] == placeholder || notASecret(s[v:end]) {
		return 0, 0, false
	}
	return v, end, true
}

// opensMember reports whether the key at i stands where an object, a map,
// a list or an argument list opens a member: after `{`, `,`, `(` or `[`,
// blanks aside. Anywhere else a key and a colon mid-line are text: prose,
// or more often the inside of a string ("Password: " in input("Password: "),
// "invalid token: " + t), whose closing quote would otherwise be read as
// the value's opening one and run on to the next quote, which a review
// found on the next line, inside the value of the assignment there.
func opensMember(s string, floor, i int) bool {
	for i > floor && isBlank(s[i-1]) {
		i--
	}
	return i > floor && strings.IndexByte("{,([", s[i-1]) >= 0
}

// notASecret reports whether a key's value, quoted or not, with a trailing
// `,` aside, is a reference to a secret or a placeholder for one rather
// than the secret: a GitHub Actions or template expression (${{ secrets.X }},
// {{ .Values.x }}), a variable ($TOKEN, ${TOKEN}), an angle-bracket token
// (<pad>, <your-token>), a type name (string, String, Option<String>), or
// kubectl describe's size of a secret (5 bytes).
// Removing them hides nothing, and they are what code, CI files and
// tokenizer configs hold. So is a value that only opens a block: {, [, a
// YAML | or >-.
func notASecret(v string) bool {
	if strings.HasSuffix(v, ",") || strings.HasSuffix(v, ";") {
		v = v[:len(v)-1]
	}
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		v = v[1 : len(v)-1]
	}
	return secretReferencePattern.MatchString(v)
}

var secretReferencePattern = regexp.MustCompile(`^(?:\$?\{\{[^\n]*\}\}|\$\{[^}\n]*\}|\$[A-Za-z_][A-Za-z0-9_]*|<[^<>\t\n ]+>|string|str|number|int|integer|bool|boolean|any|unknown|bytes|float|double|char|String|[A-Za-z_][A-Za-z0-9_:]*<[^\n]*>|[0-9]+ bytes|[{\[(|>+-]+)$`)

// startsLine reports whether only blanks and an optional `-` list marker
// stand between the start of a line and i, reading nothing before floor.
func startsLine(s string, floor, i int) bool {
	for i > floor && isBlank(s[i-1]) {
		i--
	}
	if i > floor && s[i-1] == '-' {
		i--
		for i > floor && isBlank(s[i-1]) {
			i--
		}
	}
	return i == 0 || (i > floor && s[i-1] == '\n')
}

// quotedStringEnd is the end of the quoted string at v, read as
// closingQuote reads one but on its own line only, with any part that
// follows it directly in the same quote (a quote written twice, as YAML and
// PowerShell escape one; three quotes read this way too). Unlike
// quotedWordEnd it takes nothing joined to it: in {"password":"x",
// "user":"y"} the value ends at the quote. A string that does not close on
// its own line is no value, -1: a key's value never reaches the next line.
func quotedStringEnd(s string, v int) int {
	var closer quoteCloser
	i := closer.close(s, v, 0)
	if i < 0 {
		return -1
	}
	for i < len(s) && s[i] == s[v] {
		j := closer.close(s, i, 0)
		if j < 0 {
			break
		}
		i = j
	}
	return i
}

// redactSecretFlags replaces the value of a long flag whose name is a
// secret's when the value is the next word: curl --api-key VALUE,
// mysql --password VALUE. The flag is `--` and a name whose last segment
// is a secret's word (secretFlagName); the value is the next word after
// blanks on the same line, read as an assignment's is, and is not one when
// it starts with `-` (the next flag), `=` (the assignment rule's), `<`, `>`
// or `|` (a redirection or a pipe) or `$(` (a command's output), or when it
// is the flag's own name in capitals (a help text's metavar). A value of lowercase letters only is kept: "use the --password
// flag" and "pass --token to the command" are prose, and the cost is a
// password of lowercase letters only, which keeps its value written this
// way. A single-dash flag (-p) is not read: what follows it is a password
// only for some commands.
func redactSecretFlags(s string) string {
	i := strings.Index(s, "--")
	if i < 0 {
		return s
	}
	var b strings.Builder
	last, changed := 0, false
	for i >= 0 {
		next := i + 2
		start, end, nameEnd, ok := secretFlagValueAt(s, i)
		if ok {
			b.WriteString(s[last:start])
			b.WriteString(placeholder)
			last, next, changed = end, end, true
		} else if nameEnd > next {
			next = nameEnd
		}
		j := strings.Index(s[next:], "--")
		if j < 0 {
			break
		}
		i = next + j
	}
	if !changed {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// secretFlagValueAt reads the flag whose `--` is at i and returns where a
// secret value starts and ends, and where the flag's name ends. What
// stands before the `--` is not asked: a later step can remove it
// (bob@example.com--token x), and a second pass would then find a flag the
// first did not. A name is read whole, across `-`, as the assignment rule
// reads one, so a `--` inside it starts no flag of its own, and a scan that
// finds no flag resumes past the name: reading each `--` of --a--a--a...
// to the end of the run would be quadratic in it.
func secretFlagValueAt(s string, i int) (start, end, nameEnd int, ok bool) {
	n := i + 2
	if n >= len(s) || !isNameStart(s[n]) {
		return 0, 0, 0, false
	}
	j := n
	for j < len(s) && isNameChar(s[j]) {
		j++
	}
	if !isWordChar(s[j-1]) || j >= len(s) || !isBlank(s[j]) {
		return 0, 0, j, false
	}
	name := s[n:j]
	if !secretFlagName(name) {
		return 0, 0, j, false
	}
	v := j
	for v < len(s) && isBlank(s[v]) {
		v++
	}
	if v >= len(s) || isSpace(s[v]) || strings.IndexByte("-=<>|", s[v]) >= 0 || strings.HasPrefix(s[v:], "$(") {
		return 0, 0, j, false
	}
	if s[v] == '"' || s[v] == '\'' {
		end = quotedWordEnd(s, v)
	} else {
		end = trimValueTail(s, v, unquotedRunEnd(s, v))
		if lowercaseWord(s[v:end]) || metavar(s[v:end], name) {
			return 0, 0, j, false
		}
	}
	if end == v || s[v:end] == placeholder {
		return 0, 0, j, false
	}
	return v, end, j, true
}

// secretFlagName reports whether a long flag's name says its value is a
// secret: the assignment rule's words, read in the flag's LAST segment only
// (or that segment's last camelCase word), because a flag's last word names
// what it takes: --password and --api-key take a secret, while --key-name,
// --secret-id, --target-key-id, --passphrase-file, --api-key-file,
// --token-ttl and --password-stdin take a name, an id, a path, a duration
// or nothing. A --with- or --no- flag is a switch and takes no value.
// KEY and PASS count as in secretName: in a name of capitals or of two or
// more segments or words.
func secretFlagName(name string) bool {
	if hasPrefixFold(name, "with-") || hasPrefixFold(name, "no-") {
		return false
	}
	upper := strings.ToUpper(name)
	if secretWholeNames[upper] {
		return true
	}
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' || r == '.' })
	last := parts[len(parts)-1]
	hump := lastHump(last)
	qualified := name == upper || len(parts) >= 2 || hump != last
	for _, seg := range []string{strings.ToUpper(last), strings.ToUpper(hump)} {
		if secretNameSegments[seg] || (qualified && secretNameSegmentsQualified[seg]) {
			return true
		}
	}
	return false
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && asciiEqualFold(s[:len(prefix)], prefix)
}

// metavar reports whether value is the flag's own name in capitals, as help
// text writes the value a flag takes: --token TOKEN, --api-key API_KEY.
func metavar(value, name string) bool {
	if value != strings.ToUpper(value) {
		return false
	}
	norm := func(x string) string { return strings.ReplaceAll(strings.ToUpper(x), "-", "_") }
	return norm(value) == norm(name)
}

func lowercaseWord(w string) bool {
	for i := 0; i < len(w); i++ {
		if !isLower(w[i]) {
			return false
		}
	}
	return true
}
