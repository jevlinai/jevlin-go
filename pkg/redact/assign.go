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
// and TOKENIZER_PATH are left alone. A segment written in camelCase also
// counts by its last hump (lastHump): accessToken, clientSecret, dbPassword,
// .npmrc's _authToken, apiKey.
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
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' || r == '.' })
	qualified := name == upper || len(parts) >= 2
	for _, part := range parts {
		seg := strings.ToUpper(part)
		if secretNameSegments[seg] || (qualified && secretNameSegmentsQualified[seg]) {
			return true
		}
		if hump := lastHump(part); hump != part {
			seg = strings.ToUpper(hump)
			if secretNameSegments[seg] || secretNameSegmentsQualified[seg] {
				return true
			}
		}
	}
	return false
}

// lastHump is the last camelCase word of part: from the last capital that
// follows a lowercase letter or a digit (authToken, oauth2Token, dbPASSWORD),
// or that ends a run of capitals and starts a word (APIKey), to the end. A
// part with no such capital is its own last hump.
//
// The last hump only, because in camelCase the last word names what the
// value is and a word before it says which: accessToken and clientSecret
// hold a token and a secret, while tokenCount, keyName, passThrough and
// secretName hold a count, a name, a flag and a name. Being a hump of a
// name of two or more, KEY and PASS count here as they do in a name of two
// or more segments: apiKey, dbPass, and also primaryKey and sortKey, which
// lose a value that is rarely a secret, as primary_key and sort_key already
// did.
func lastHump(part string) string {
	for i := len(part) - 1; i > 0; i-- {
		c, prev := part[i], part[i-1]
		if !isUpper(c) {
			continue
		}
		if isLower(prev) || isDigit(prev) || (isUpper(prev) && i+1 < len(part) && isLower(part[i+1])) {
			return part[i:]
		}
	}
	return part
}

func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }
func isLower(c byte) bool { return c >= 'a' && c <= 'z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// redactSecretAssignments replaces the value of every assignment whose name
// says it is a secret (secretName) or is this client's own trace bridge.
//
// A name is [A-Za-z_] followed by letters, digits, `_`, `-` and `.`, ending
// in a letter, digit or `_`, and starting where the character before it is
// not a letter, digit or `_`; it is written immediately before `=`. After
// `$env:` (PowerShell), and for the trace bridge in any syntax, spaces or
// tabs may stand on either side of the `=`; so they may in PowerShell's
// braced form, ${env:NAME} = '...'.
//
// The value, when it starts with a quote, is read as a shell reads a word
// (quotedWordEnd): quoted parts and the characters joined to them, up to
// whitespace, `&` or `;`. The first part may run across at most
// quotedValueMaxLines line breaks, and when it does not close, the value is
// the rest of its line. Otherwise the value is everything up to whitespace,
// `&` or `;`, a backslash keeping the character after it (correct\ horse).
// Either way any trailing `,`, and any trailing `)` `]` `}` or quote that
// the value itself did not open, is handed back to the text around it,
// because those close something the value sits inside: f(password=pw),
// "TOKEN=x", ?access_token=x&page=2. A value that starts with `=` is a
// comparison (a==b) and is not one. And when the word that holds the name
// opened with a quote that is still open (wordScan), the value runs on to
// where that quote closes on its line, or to a `;` or `&` before it
// (enclosedValueEnd): -e "DB_PASSWORD=correct horse battery".
func redactSecretAssignments(s string) string {
	e := strings.IndexByte(s, '=')
	if e < 0 {
		return s
	}
	var b strings.Builder
	words := wordScan{escaped: quoteSearch{escaped: true}}
	last, changed := 0, false
	for e >= 0 {
		next := e + 1
		if start, end, ok := secretValueAt(s, last, e, &words); ok {
			b.WriteString(s[last:start])
			b.WriteString(placeholder)
			last, next, changed = end, end, true
			words.advance(s, start)
			words.skip(end)
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
// secret value starts and ends. words has read the text before every name
// an earlier call looked at, and is moved on to this one's.
func secretValueAt(s string, floor, e int, words *wordScan) (start, end int, ok bool) {
	k := e
	for k > floor && isBlank(s[k-1]) {
		k--
	}
	p, name, psEnv := -1, "", false
	if k > floor && s[k-1] == '}' {
		// PowerShell's braced form, ${env:NAME}, spaces around = allowed.
		r := k - 1
		for r > floor && isNameChar(s[r-1]) {
			r--
		}
		if r == k-1 || !isNameStart(s[r]) || !isWordChar(s[k-2]) || r < 6 || !asciiEqualFold(s[r-6:r], "${env:") {
			return 0, 0, false
		}
		p, name, psEnv = r, s[r:k-1], true
	} else {
		r := k
		for r > floor && isNameChar(s[r-1]) {
			r--
		}
		if r == k || !isWordChar(s[k-1]) {
			return 0, 0, false
		}
		for q := r; q < k; q++ {
			if isNameStart(s[q]) && (q == 0 || !isWordChar(s[q-1])) {
				p = q
				break
			}
		}
		if p < 0 {
			return 0, 0, false
		}
		name = s[p:k]
		psEnv = p >= 5 && asciiEqualFold(s[p-5:p], "$env:")
	}
	bridge := isBridgeName(name)
	v := e + 1
	for v < len(s) && isBlank(s[v]) {
		v++
	}
	// Blanks around `=` belong to PowerShell and the bridge, and to any
	// name whose value is quoted: TOML's and Python's password = "x". An
	// unquoted value after them is code (token = get_token()) or prose.
	quoted := v < len(s) && (s[v] == '"' || s[v] == '\'')
	if !psEnv && !bridge && !quoted {
		if k < e {
			return 0, 0, false
		}
		v = e + 1
	}
	if !bridge && !secretName(name) && !sasSignature(s, p, name) {
		return 0, 0, false
	}
	words.advance(s, p)
	if v < len(s) && !isSpace(s[v]) && s[v] != '=' && isCmdSet(s, floor, p, name) {
		end = cmdValueEnd(s, v)
	} else {
		end = secretValueEnd(s, v, words)
	}
	if end == v || s[v:end] == placeholder {
		return 0, 0, false
	}
	return v, end, true
}

// isCmdSet reports whether the name at p is set by cmd's `set NAME=value`,
// which takes the rest of the line as the value, spaces and quotes and all. It counts
// only where cmd would read it as a command: `set` (any letter case, after
// an optional @) at the start of a line, or after a backtick, `&`, `(` or
// `|`, and a name with no lowercase letter. "You can set password=x in the
// shell" and a line that begins "set password=x and restart" are prose,
// and a line-long value there would take the rest of the sentence. Nothing
// before floor is read: a removed value is a placeholder on the next pass.
func isCmdSet(s string, floor, p int, name string) bool {
	if name != strings.ToUpper(name) || p == 0 || !isBlank(s[p-1]) {
		return false
	}
	j := p
	for j > 0 && isBlank(s[j-1]) {
		j--
	}
	if j-3 < floor || !asciiEqualFold(s[j-3:j], "set") {
		return false
	}
	k := j - 3
	if k > floor && s[k-1] == '@' {
		k--
	}
	for k > floor && isBlank(s[k-1]) {
		k--
	}
	return k == 0 || (k > floor && strings.IndexByte("\n`&(|", s[k-1]) >= 0)
}

// cmdValueEnd is where cmd's `set` value ends: at the end of its line, or
// at `&`, `|` or a backtick, which end the command or the code span it is
// written in; trailing blanks and what trimValueTail hands back stay
// outside.
func cmdValueEnd(s string, v int) int {
	end := v
	for end < len(s) && strings.IndexByte("\n\r&|`", s[end]) < 0 {
		end++
	}
	for end > v && isBlank(s[end-1]) {
		end--
	}
	return trimValueTail(s, v, end)
}

// wordScan follows, left to right, the word that holds each name: what
// follows the last whitespace, with a removed value counted as part of the
// word, since its placeholder has none. A word that opens with a quote,
// after any ( [ or {, holds the values of the names in it inside that quote
// for as long as the quote stays open (an even number of the same quote
// since): -e "DB_PASSWORD=correct horse battery", "Server=db;Password=a b",
// ["API_TOKEN=a b"]. Only the word is read, not the line before it, and
// what a removed value held is not read at all: a count of the quotes
// before a name reaches back over text a step removes, on this pass or a
// later one, and a second pass would then read a different quote as open.
type wordScan struct {
	pos   int
	begun bool // past the word's leading ( [ {
	head  byte // the quote the word opens with, or 0
	open  bool // that quote is open where the scan has reached

	escaped, plain quoteSearch // enclosedValueEnd's two searches
}

func (w *wordScan) advance(s string, to int) {
	for ; w.pos < to; w.pos++ {
		switch c := s[w.pos]; {
		case isSpace(c):
			w.begun, w.head, w.open = false, 0, false
		case !w.begun && (c == '(' || c == '[' || c == '{'):
		case !w.begun:
			w.begun = true
			if c == '"' || c == '\'' {
				w.head, w.open = c, true
			}
		case c == w.head:
			w.open = !w.open
		}
	}
}

// skip moves past a removed value without reading it; the scan must have
// advanced to the value's start. What stands there now is the placeholder,
// which begins a word when none has begun (its [ is a bracket, its R is
// not a quote) and changes no quote.
func (w *wordScan) skip(to int) {
	if to > w.pos {
		w.pos = to
	}
	w.begun = true
}

// enclosing is the quote that holds the value of a name the scan has
// reached, or 0.
func (w *wordScan) enclosing() byte {
	if w.open {
		return w.head
	}
	return 0
}

func secretValueEnd(s string, v int, words *wordScan) int {
	if v >= len(s) {
		return v
	}
	from := v
	switch s[v] {
	case '"', '\'':
		from = quotedWordEnd(s, v)
	case '=', ' ', '\t', '\n', '\f', '\r':
		// A comparison (a==b), or no value at all (set NAME= to clear it):
		// a quote that holds the name does not reach past the blank.
		return v
	}
	if q := words.enclosing(); q != 0 {
		if end := enclosedValueEnd(s, from, q, words); end >= 0 {
			return end
		}
	}
	if from > v {
		return from
	}
	return trimValueTail(s, v, unquotedRunEnd(s, v))
}

// enclosedValueEnd is where a value ends inside the quote q that holds it
// (wordScan): at the quote that closes q on its line, or at a `;` or `&`
// before that, which separate the settings of a connection string or a
// query; trailing blanks and what trimValueTail hands back stay outside. A
// quoted value reads its own quotes first and from is where they end, so a
// second pass over its placeholder finds the same end. The closing quote is
// the first q on the line that no backslash (and, for a double quote, no
// backtick) stands right before, else the first q: the character before is
// all that is asked, because a search that kept track of escape pairs
// would answer differently from different starts and could not remember
// its answer. It returns -1 when q does not close on this line, and the
// value is then read as it would be without it.
func enclosedValueEnd(s string, from int, q byte, w *wordScan) int {
	end := w.escaped.find(s, from, q)
	if end < 0 {
		if end = w.plain.find(s, from, q); end < 0 {
			return -1
		}
	}
	if i := strings.IndexAny(s[from:end], "&;"); i >= 0 {
		end = from + i
	}
	for end > from && isBlank(s[end-1]) {
		end--
	}
	return trimValueTail(s, from, end)
}

// escapedAt reports whether the quote at i has a backslash right before it,
// or for a double quote a backtick.
func escapedAt(s string, i int) bool {
	return i > 0 && (s[i-1] == '\\' || (s[i] == '"' && s[i-1] == '`'))
}

// quoteSearch finds the first q at or after v on v's line, when escaped is
// set one with no backslash (or for a double quote no backtick) right
// before it, and remembers its last answer, which holds for any later start
// up to it: the names of one long word would otherwise each search the
// rest of the line.
type quoteSearch struct {
	escaped        bool
	used           bool
	q              byte
	from, at, stop int
}

func (f *quoteSearch) find(s string, v int, q byte) int {
	if f.used && f.q == q && v >= f.from && (f.at >= v || (f.at < 0 && v <= f.stop)) {
		return f.at
	}
	f.used, f.q, f.from, f.at = true, q, v, -1
	i := v
	for ; i < len(s) && s[i] != '\n'; i++ {
		if s[i] == q && (!f.escaped || !escapedAt(s, i)) {
			f.at = i
			break
		}
	}
	f.stop = i
	return f.at
}

// quotedWordEnd reads the value that starts with the quote at v the way a
// shell reads a word, because that is how the forms that hide part of a
// value are put together: quoted parts next to each other are one value
// (PowerShell writes a quote inside a single-quoted string as two, and
// Python's triple quotes are three), a backslash before a quote between
// them keeps it (POSIX ends the string, writes \' and opens another), and
// characters joined to a part are part of it. The word ends at whitespace,
// `&` or `;` outside the quotes, with trimValueTail applied to what follows
// the last quoted part.
//
// A value that opens with three quotes runs to the next three of the same,
// within quotedValueMaxLines line breaks. Otherwise the first part runs to
// its closing quote (closingQuote) within that many line breaks. When the
// first part does not close, the value is the rest of its line. A later part must close
// on its own line, or its quote is an ordinary character: a stray quote
// after a value must not reach into the lines that follow it.
func quotedWordEnd(s string, v int) int {
	if i := quotedWordEndWithin(s, v, quotedValueMaxLines); i >= 0 {
		return i
	}
	return lineEnd(s, v)
}

// quotedWordEndWithin is quotedWordEnd with the first part allowed maxLines
// line breaks, and -1 when it does not close within them.
func quotedWordEndWithin(s string, v, maxLines int) int {
	var closer quoteCloser
	var i int
	if opensThreeQuotes(s, v) {
		i = tripleQuoteEnd(s, v, maxLines)
	} else {
		i = closer.close(s, v, maxLines)
	}
	if i < 0 {
		return -1
	}
	last := i
	for i < len(s) {
		switch c := s[i]; {
		case c == '"' || c == '\'':
			if closed := closer.close(s, i, 0); closed >= 0 {
				i, last = closed, closed
				continue
			}
			i++
		case c == '\\' && i+1 < len(s) && s[i+1] != '\n' && s[i+1] != '\r':
			i += 2
		case isSpace(c) || c == '&' || c == ';':
			return trimValueTail(s, last, i)
		default:
			i++
		}
	}
	return trimValueTail(s, last, i)
}

func opensThreeQuotes(s string, v int) bool {
	return v+3 <= len(s) && s[v+1] == s[v] && s[v+2] == s[v]
}

// tripleQuoteEnd returns the index just past the three quotes that close the
// three at v, or -1 when they do not close within maxLines line breaks.
func tripleQuoteEnd(s string, v, maxLines int) int {
	q, lines := s[v], 0
	for i := v + 3; i+2 < len(s); i++ {
		switch s[i] {
		case q:
			if s[i+1] == q && s[i+2] == q {
				return i + 3
			}
		case '\n':
			if lines++; lines > maxLines {
				return -1
			}
		}
	}
	return -1
}

// closingQuote (quoteCloser's close) returns the index just past the quote
// that closes the one at v, or -1 when there is none within maxLines line
// breaks.
//
// Whether a backslash escapes depends on the language, and the text does not
// say which it is: in Python, JSON and a POSIX double-quoted string it does,
// in a POSIX or PowerShell single-quoted string and a TOML literal it does
// not. So the escaping reading (a backslash escapes the next character, and
// in a double-quoted string so does PowerShell's backtick) is taken when it
// closes the quote on its own line, being the longer of the two; when it
// does not, the plain reading is: the next quote of the same kind. Taken the
// other way round, SSH_KEY_DIR='C:\keys\' would run on into the next line
// and stop inside the value assigned there, leaving that value's tail.
//
// A quoteCloser reads the parts of one value one after another,
// remembering for each kind of quote where the escaping reading last
// failed. It fails when no unescaped quote of that kind stands on the
// rest of the line, so every such quote there is the second character of an
// escape pair, and a part that opens at one of them reads the same pairs
// after it and fails too: `"`" repeated is linear, not a search of the rest
// of the line from every quote.
type quoteCloser struct {
	failed [2]bool
	from   [2]int // where the failed reading's quote was
	stop   [2]int // where it stopped: the end of that line
}

func (c *quoteCloser) close(s string, v, maxLines int) int {
	k := 0
	if s[v] == '\'' {
		k = 1
	}
	if !c.failed[k] || v <= c.from[k] || v > c.stop[k] {
		i, stop := escapedClosingQuote(s, v)
		if i >= 0 {
			return i
		}
		c.failed[k], c.from[k], c.stop[k] = true, v, stop
	}
	q, lines := s[v], 0
	for i := v + 1; i < len(s); i++ {
		switch s[i] {
		case q:
			return i + 1
		case '\n':
			if lines++; lines > maxLines {
				return -1
			}
		}
	}
	return -1
}

// escapedClosingQuote is closingQuote's escaping reading, on v's own line:
// the index just past the closing quote, or -1 and where the line ends.
func escapedClosingQuote(s string, v int) (int, int) {
	q := s[v]
	for i := v + 1; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\n':
			return -1, i
		case c == '\\' || (c == '`' && q == '"'):
			if i+1 < len(s) && s[i+1] != '\n' {
				i++
			}
		case c == q:
			return i + 1, 0
		}
	}
	return -1, len(s)
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

// unquotedRunEnd is the end of an unquoted value: whitespace, `&` or `;`,
// except where a backslash keeps the character after it, as a shell does
// (correct\ horse\ battery is one word). A backslash before a line break is
// not one of those: the value ends with its line.
func unquotedRunEnd(s string, i int) int {
	for i < len(s) {
		c := s[i]
		if c == '\\' && i+1 < len(s) && s[i+1] != '\n' && s[i+1] != '\r' {
			i += 2
			continue
		}
		if isSpace(c) || c == '&' || c == ';' {
			break
		}
		i++
	}
	return i
}

// trimValueTail hands back the trailing characters of s[from:to] that close
// something opened before the value: a `,`, and a `)` `]` `}`, quote or
// backtick (a Markdown code span) the value has more of than it opened.
func trimValueTail(s string, from, to int) int {
	var paren, bracket, brace, dquote, squote, backtick int
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
		case '`':
			backtick++
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
		case c == '`' && backtick%2 == 1:
			backtick--
		default:
			return to
		}
		to--
	}
	return to
}

// sasSignature reports whether name is a URL query's sig parameter, which
// in an Azure shared access signature is the credential itself
// (?sv=...&sig=...). Only after `?` or `&`: sig= elsewhere is anyone's
// word for anything.
func sasSignature(s string, p int, name string) bool {
	return p > 0 && (s[p-1] == '?' || s[p-1] == '&') && asciiEqualFold(name, "sig")
}

// isBridgeName reports whether name is the trace bridge's, alone or as the
// last part of a dotted or hyphenated name. The suffix counts because a
// later step can remove what stands before it: in
// bob@example.com.JEVLIN_TRACE_BRIDGE=… the name is read whole, the email
// step then removes bob@example.com, and a second pass would find the
// bridge's name standing alone. Every secret word already has that
// property, being a segment; this gives it to the bridge's exact name.
func isBridgeName(name string) bool {
	n := len(traceBridgeEnvName)
	if len(name) < n || !asciiEqualFold(name[len(name)-n:], traceBridgeEnvName) {
		return false
	}
	return len(name) == n || name[len(name)-n-1] == '.' || name[len(name)-n-1] == '-'
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
