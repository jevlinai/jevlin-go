package redact

import "strings"

// The secret-assignment rule: a password has no shape, so the name it is
// assigned to is what gives it away. TraceText applies it through
// redactSecretAssignments; the log path does not.
//
// It is a scanner rather than one regular expression because a regular
// expression's matches do not overlap: a value that is not a secret's
// would swallow a secret chained after it (`?user=fred&password=…`,
// `--env=DB_PASSWORD=…`, `Server=db;Password=…`). Here a name that is not
// a secret's consumes nothing, and every `=` is read for the name in front
// of it, left to right, once. pkg/redact and cmd/jevlin/agent_trace_common.js
// implement the same scanner byte for byte over ASCII decisions.

// secretNameSegments are the words that make an assignment's value a
// secret when they are a whole segment of its name, in any letter case. A
// name's segments are what `_`, `-` and `.` separate: DATABASE_PASSWORD,
// api-token, db.password, PGPASSWORD. A segment, not a substring, so MONKEY
// and TOKENIZER_PATH are left alone.
var secretNameSegments = map[string]bool{
	"PASSWORD": true, "PASSWD": true, "PASSPHRASE": true, "PGPASSWORD": true,
	"SECRET": true, "SECRETS": true, "TOKEN": true,
	"CREDENTIAL": true, "CREDENTIALS": true, "APIKEY": true,
}

// secretNameSegmentsQualified count only in a name with no lowercase letter
// (API_KEY, DB_PASS) or a name of two or more segments (api_key, api-key,
// secret-key, db_pass). `key=value`, `--key=2` and `pass=2` are everyday
// prose and flag syntax, and stay.
var secretNameSegmentsQualified = map[string]bool{"KEY": true, "PASS": true}

// secretWholeNames are well-known variables whose secret word is not a
// segment of their own. PWD alone is the working directory and stays.
var secretWholeNames = map[string]bool{"MYSQL_PWD": true}

// traceBridgeEnvName is this client's own trace bridge variable. Its value
// is an encoded envelope that can itself hold earlier assistant text, and
// no pattern can see into it, so a quoted command line that carries it has
// the value removed whatever it looks like, in any letter case and with
// spaces around `=` as PowerShell writes it.
const traceBridgeEnvName = "JEVLIN_TRACE_BRIDGE"

// quotedValueMaxLines is how many line breaks a quoted secret value may
// cross before its closing quote: a PEM private key quoted in a .env file
// runs to about fifty lines at 4096 bits. A quote that does not close
// within it is treated as never closing, and the value runs to the end of
// its own line instead.
const quotedValueMaxLines = 100

func secretName(name string) bool {
	upper := strings.ToUpper(name)
	if secretWholeNames[upper] {
		return true
	}
	segs := strings.FieldsFunc(upper, func(r rune) bool { return r == '_' || r == '-' || r == '.' })
	qualified := name == upper || len(segs) >= 2
	for _, seg := range segs {
		if secretNameSegments[seg] || (qualified && secretNameSegmentsQualified[seg]) {
			return true
		}
	}
	return false
}

// redactSecretAssignments replaces the value of every assignment whose name
// says it is a secret (secretName) or is this client's own trace bridge.
//
// A name is [A-Za-z_] followed by letters, digits, `_`, `-` and `.`, ending
// in a letter, digit or `_`, and starting where the character before it is
// not a letter, digit or `_`; it is written immediately before `=`. After
// `$env:` (PowerShell), and for the trace bridge in any syntax, spaces or
// tabs may stand on either side of the `=`.
//
// The value is, when it starts with a quote, everything to the matching
// unescaped quote (a backslash escapes the next character), across at most
// quotedValueMaxLines line breaks, and then any unquoted characters joined
// to it; when that quote does not close, the rest of its line. Otherwise
// it is everything up to whitespace, `&` or `;`, with any trailing `,`, and
// any trailing `)` `]` `}` or quote that the value itself did not open,
// handed back to the text around it, because those close something the
// value sits inside: f(password=pw), "TOKEN=x", ?access_token=x&page=2.
// A value that starts with `=` is a comparison (a==b) and is not one.
func redactSecretAssignments(s string) string {
	e := strings.IndexByte(s, '=')
	if e < 0 {
		return s
	}
	var b strings.Builder
	last, changed := 0, false
	for e >= 0 {
		next := e + 1
		if start, end, ok := secretValueAt(s, last, e); ok {
			b.WriteString(s[last:start])
			b.WriteString(placeholder)
			last, next, changed = end, end, true
		}
		i := strings.IndexByte(s[next:], '=')
		if i < 0 {
			break
		}
		e = next + i
	}
	if !changed {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// secretValueAt reads the name in front of the `=` at e, never reaching
// back past floor (the end of the last value removed), and returns where a
// secret value starts and ends.
func secretValueAt(s string, floor, e int) (start, end int, ok bool) {
	k := e
	for k > floor && isBlank(s[k-1]) {
		k--
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
	name := s[p:k]
	psEnv := p >= 5 && asciiEqualFold(s[p-5:p], "$env:")
	bridge := asciiEqualFold(name, traceBridgeEnvName)
	if k < e && !psEnv && !bridge {
		return 0, 0, false
	}
	if !bridge && !secretName(name) {
		return 0, 0, false
	}
	v := e + 1
	if psEnv || bridge {
		for v < len(s) && isBlank(s[v]) {
			v++
		}
	}
	end = secretValueEnd(s, v)
	if end == v || s[v:end] == placeholder {
		return 0, 0, false
	}
	return v, end, true
}

func secretValueEnd(s string, v int) int {
	if v >= len(s) {
		return v
	}
	switch s[v] {
	case '"', '\'':
		closed := closingQuote(s, v)
		if closed < 0 {
			return lineEnd(s, v)
		}
		return trimValueTail(s, closed, unquotedRunEnd(s, closed))
	case '=':
		return v
	}
	return trimValueTail(s, v, unquotedRunEnd(s, v))
}

// closingQuote returns the index just past the unescaped quote that closes
// the one at v, or -1 when there is none within quotedValueMaxLines lines.
func closingQuote(s string, v int) int {
	q, lines := s[v], 0
	for i := v + 1; i < len(s); i++ {
		c := s[i]
		if c == '\\' {
			i++
			if i >= len(s) {
				break
			}
			c = s[i]
		} else if c == q {
			return i + 1
		}
		if c == '\n' {
			if lines++; lines > quotedValueMaxLines {
				return -1
			}
		}
	}
	return -1
}

// lineEnd is the end of the line v is on, before its "\r\n" or "\n".
func lineEnd(s string, v int) int {
	i := strings.IndexByte(s[v:], '\n')
	if i < 0 {
		return len(s)
	}
	end := v + i
	if end > v && s[end-1] == '\r' {
		end--
	}
	return end
}

func unquotedRunEnd(s string, i int) int {
	for i < len(s) && !isSpace(s[i]) && s[i] != '&' && s[i] != ';' {
		i++
	}
	return i
}

// trimValueTail hands back the trailing characters of s[from:to] that close
// something opened before the value: a `,`, and a `)` `]` `}` or quote the
// value has more of than it opened.
func trimValueTail(s string, from, to int) int {
	var paren, bracket, brace, dquote, squote int
	for i := from; i < to; i++ {
		switch s[i] {
		case '(':
			paren--
		case ')':
			paren++
		case '[':
			bracket--
		case ']':
			bracket++
		case '{':
			brace--
		case '}':
			brace++
		case '"':
			dquote++
		case '\'':
			squote++
		}
	}
	for to > from {
		switch c := s[to-1]; {
		case c == ',':
		case c == ')' && paren > 0:
			paren--
		case c == ']' && bracket > 0:
			bracket--
		case c == '}' && brace > 0:
			brace--
		case c == '"' && dquote%2 == 1:
			dquote--
		case c == '\'' && squote%2 == 1:
			squote--
		default:
			return to
		}
		to--
	}
	return to
}

func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

// isSpace is Go's regexp \s: [\t\n\f\r ].
func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r' }

func isNameStart(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func isNameChar(c byte) bool { return isWordChar(c) || c == '-' || c == '.' }
