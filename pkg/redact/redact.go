// Package redact is the single place a log handler is constructed and the
// choke point every log line passes through. No credential, header value, or
// URL userinfo may reach a log sink, whatever path it takes to get there:
// slog attributes, error strings, or the pre-formatted lines net/http writes
// to its *log.Logger fields. No package outside this one and cmd/ may build a
// slog.Handler or a *log.Logger.
package redact

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const placeholder = "[REDACTED]"

// Attribute keys whose values are never rendered, matched case-insensitively.
var keyDenylist = map[string]struct{}{
	"authorization":       {},
	"api_key":             {},
	"token":               {},
	"cookie":              {},
	"set-cookie":          {},
	"proxy-authorization": {},
}

var (
	// Provider/router key material: sk-or-v1-…, sk-ant-…, a dashless
	// vendor key, and this system's own sr- router key. Requires >=16
	// chars after the prefix-dash, not just 6: a short threshold reads
	// ordinary hyphenated identifiers as keys — sr-only (a CSS
	// accessibility class), sr-Latn-RS (a BCP-47 locale tag),
	// sk-cache-entry-42 (a plain cache key) all measured as false
	// positives at 6 and are excluded at 16. Every real key shape this
	// pattern exists for — vendor keys, this system's own sr- key — runs
	// well past 16 in practice.
	skPattern = regexp.MustCompile(`\b(?:sk|sr)-[A-Za-z0-9_-]{16,}`)
	// Bearer values in free text (error strings, net/http log lines).
	// String applies this; TraceText does not — see TraceText.
	bearerPattern = regexp.MustCompile(`(?i)\bbearer\s+\S+`)
	// scheme://user:pass@ userinfo embedded in a URL. The match consumes
	// the trailing "@" and the replacement does not reintroduce one —
	// deliberately, so the placeholder can never read as
	// "[REDACTED]@host", which is itself email-shaped and would
	// re-trigger emailPattern below on a second pass over the output.
	userinfoPattern = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`)
	// GitHub tokens: the short-prefix family (ghp_/gho_/ghu_/ghs_/ghr_)
	// and fine-grained personal access tokens (github_pat_…).
	githubTokenPattern = regexp.MustCompile(`\bgh[opsur]_[A-Za-z0-9]{20,}\b|\bgithub_pat_[A-Za-z0-9_]{20,}\b`)
	// AWS access key ids: a fixed shape, AKIA + 16 uppercase alnums.
	awsKeyPattern = regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)
	// Stripe's secret and restricted keys, live and test: sk_live_...,
	// rk_test_.... The publishable pk_ key is public by design.
	stripeKeyPattern = regexp.MustCompile(`\b[rs]k_(?:live|test)_[A-Za-z0-9]{16,}`)
	// A bare JWT (three base64url segments): a token pasted into free
	// text or logged directly, not only one riding after "Bearer ".
	jwtPattern = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\b`)
	// Email addresses. Applied via redactEmails, not a bare ReplaceAll —
	// see there for why: the shape is identical to an ssh/git remote
	// target, and a naive match redacts those too.
	emailPattern = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)
	// A home-directory path: only the username segment identifies
	// anyone, so only it is masked — the rest of the path (often useful
	// for debugging, e.g. which file under it) survives. Applied via
	// redactHomePaths, not a bare ReplaceAll — see there for why.
	homePathPattern = regexp.MustCompile(`(/Users/|/home/)[^/\s]+`)
	// The Windows sibling: C:\Users\<name>\... . This repo ships Windows
	// binaries; the Unix-only pattern above missed this entirely. A
	// separator may be doubled (or more), as Python's repr and JSON print a
	// path: C:\\Users\\<name>. A name with spaces in it (C:\Users\Équipe
	// Données\Documents) is taken whole when the path goes on past it: up to
	// three more words, none holding a quote, a colon, a slash or a
	// backtick, and then a backslash. A word with any of those is the next
	// path or the text around this one (C:\Users\bob and C:\Users\…), and
	// without the backslash after it nothing marks where a name ends and
	// prose begins, so there only the first word goes.
	windowsHomePathPattern = regexp.MustCompile("(?i)([A-Z]:\\\\+Users\\\\+)[^\\\\\\s]+(?:(?: [^\\\\\\s:\"'`/]+){1,3}(\\\\))?")
	// One line of an environment listing: an optional list marker (- * + >)
	// or line number (cat -n, a numbered list), an optional `export`,
	// `declare -x` or `typeset -x`, a name, `=`, and the rest of the line.
	// Applied via redactEnvDumps, to runs only.
	envLinePattern = regexp.MustCompile(`^([\t\n\f\r ]*` + envLineMarker + `(?:(?:export|(?:declare|typeset)[\t ]+-[A-Za-z]+)[\t ]+)?([A-Za-z_][A-Za-z0-9_]*)=)(.*)$`)
	// bash's `declare -x NAME` for a variable exported with no value: part
	// of the listing, with nothing to remove.
	envBareDeclarePattern = regexp.MustCompile(`^[\t\n\f\r ]*` + envLineMarker + `(?:declare|typeset)[\t ]+-[A-Za-z]+[\t ]+([A-Za-z_][A-Za-z0-9_]*)[\t\n\f\r ]*$`)
	// A second NAME= after whitespace: a logfmt record or a command line,
	// not one variable's value.
	envPairAfterSpacePattern = regexp.MustCompile(`[\t\n\f\r ][A-Za-z_][A-Za-z0-9_]*=`)
)

// PowerShell's Get-ChildItem Env: (and any hashtable it prints) is a
// table, not NAME=value lines: a Name/Value header, a rule of dashes, and
// a row per variable. Applied via redactEnvTables.
var (
	envTableHeader = regexp.MustCompile(`^[\t ]*Name[\t ]+Value[\t ]*$`)
	envTableRule   = regexp.MustCompile(`^[\t ]*-+[\t ]+-+[\t ]*$`)
	envTableRow    = regexp.MustCompile(`^([\t ]*[^\t\n\f\r ]+(?:\t|  )[\t ]*)([^\t\n\f\r ].*)$`)
	// A row with no value: one name and nothing after it but blanks. A
	// name starts with a letter or `_` and holds letters, digits and
	// `_ . ( ) -` (ProgramFiles(x86)), ending in neither `.` nor `-`.
	envTableNameOnly = regexp.MustCompile(`^[\t ]*[A-Za-z_](?:[A-Za-z0-9_.()-]*[A-Za-z0-9_)])?[\t ]*$`)
)

// redactEnvTables replaces every value in a table under a Name/Value
// header and its rule of dashes, each row's value being the rest of its
// line after the name and two or more blanks (or a tab). Two more lines
// continue a table: a name with no value (envTableNameOnly), which is how
// PowerShell prints an empty variable or a $null entry, and a line whose
// blanks reach exactly the column the header's Value starts at, which is
// how Format-Table -Wrap continues a long value; that line's text is the
// value's and goes too. A table ends at the first line that is none of
// these: an empty line, a code fence, a sentence (one blank after its first
// word), a numbered list item (1. one blank). So the text after it keeps
// its words.
// The names stay, as in redactEnvDumps. The cost: any hashtable PowerShell
// prints has the same header, and loses its values too; and a one-word line
// right after a table, being a name with no value, does not end it, so a
// line after that one shaped like a row loses its value.
func redactEnvTables(s string) string {
	if !strings.Contains(s, "Value") {
		return s
	}
	lines := strings.Split(s, "\n")
	changed := false
	for i := 0; i+1 < len(lines); i++ {
		header := strings.TrimSuffix(lines[i], "\r")
		if !envTableHeader.MatchString(header) || !envTableRule.MatchString(strings.TrimSuffix(lines[i+1], "\r")) {
			continue
		}
		column := strings.Index(header, "Value")
		k := i + 2
		for ; k < len(lines); k++ {
			line, cr := strings.TrimSuffix(lines[k], "\r"), ""
			if line != lines[k] {
				cr = "\r"
			}
			indent := 0
			for indent < len(line) && isBlank(line[indent]) {
				indent++
			}
			if indent == column && indent < len(line) {
				if line[indent:] != placeholder {
					lines[k] = line[:indent] + placeholder + cr
					changed = true
				}
				continue
			}
			if envTableNameOnly.MatchString(line) {
				continue
			}
			m := envTableRow.FindStringSubmatch(line)
			if m == nil {
				break
			}
			if m[2] != placeholder {
				lines[k] = m[1] + placeholder + cr
				changed = true
			}
		}
		i = k - 1
	}
	if !changed {
		return s
	}
	return strings.Join(lines, "\n")
}

// envDumpRun is how many consecutive NAME=value lines make an environment
// dump. One or two such lines are ordinary prose about a setting; five in
// a row is the output of env or printenv quoted back, and nothing in it
// has a shape a pattern could pick the secrets out by.
const envDumpRun = 5

// envLineMarker is what may stand before an environment line when it is
// quoted in a list, a blockquote or `cat -n` output.
const envLineMarker = `(?:(?:[-*+>]|[0-9]+[.)]?)[\t ]+)?`

// remoteAccessVerbs precede an ssh/scp/rsync/sftp destination that is
// shaped exactly like an email address (user@host) but is not one —
// "ssh deploy@prod.example.com" measured as a false positive. Checked
// against the token immediately before the match, case-insensitively.
var remoteAccessVerbs = []string{"ssh", "scp", "rsync", "sftp"}

// String scrubs credential-shaped substrings from free text — the log path.
// Order matters: userinfoPattern runs before the email pass, for the reason
// noted on userinfoPattern above.
func String(s string) string {
	s = scrubCommon(s)
	s = bearerPattern.ReplaceAllString(s, "Bearer "+placeholder)
	s = redactEmails(s)
	s = redactHomePaths(s)
	return s
}

// TraceText scrubs assistant-authored trajectory text before it leaves the
// machine (trace.go's capTrace, miner.go's saveLineage) — everything String
// does EXCEPT bearerPattern. "bearer" measured as a false positive here in
// a way it is not for logs: ordinary prose about tokens ("bearer tokens
// expire soon") is common in a model's visible narration and is exactly
// the trajectory data the product exists to collect, whereas a log line
// is far more likely to actually contain a header value. A real
// Authorization: Bearer credential is still caught here if it has a
// recognizable shape (sk-/sr-/JWT/AWS/GitHub); only the generic
// "bearer <word>" catch-all is skipped.
//
// It also covers a PEM private key block, whatever stands around it, and
// what an assistant writes around a search that has no credential shape
// at all: an environment dump, a secret assigned by name
// (NAME=value, a key and a colon, a long flag and its value),
// this client's own trace bridge, the machine's hostname wherever it stands
// as a word, and the account name where the text uses it as one. The log
// path does not need those: a log line is this client's own words, not a
// model's account of what it just read.
func TraceText(s string) string {
	s = redactPrivateKeyBlocks(s)
	s = scrubCommon(s)
	s = redactSecretFlags(s)
	s = redactSecretKeys(s)
	s = redactSecretAssignments(s)
	s = redactEnvDumps(s)
	s = redactEnvTables(s)
	s = redactEmails(s)
	s = redactAccountHomes(s)
	s = redactHomePaths(s)
	s = redactLocalIdentity(s)
	return s
}

// redactEnvDumps replaces every value in a run of consecutive environment
// lines that names at least envDumpRun distinct variables. The names stay:
// they say what kind of output this was, and they identify nobody.
//
// A line does not count, and breaks a run, when its value starts with `=`
// (a pinned requirement, name==1.2), ends with `,` (a keyword argument or
// a diff of one), or carries another NAME= after whitespace (a logfmt
// record). That is what keeps code and logs that are only shaped like a
// listing; the cost is that an environment variable whose own value holds
// " NAME=" splits a real listing there and that one line is kept. A run
// of fewer distinct names is a loop's output (i=0, i=1, ...), not a dump.
func redactEnvDumps(s string) string {
	if !strings.Contains(s, "=") {
		return s
	}
	lines := strings.Split(s, "\n")
	names := make([]string, len(lines))
	isEnv := make([]bool, len(lines))
	for i, raw := range lines {
		line := strings.TrimSuffix(raw, "\r")
		if m := envLinePattern.FindStringSubmatch(line); m != nil {
			v := m[3]
			isEnv[i] = !strings.HasPrefix(v, "=") && !strings.HasSuffix(strings.TrimRight(v, " \t"), ",") && !envPairAfterSpacePattern.MatchString(v)
			names[i] = m[2]
		} else if m := envBareDeclarePattern.FindStringSubmatch(line); m != nil {
			isEnv[i], names[i] = true, m[1]
		}
	}
	changed := false
	for i := 0; i < len(lines); {
		if !isEnv[i] {
			i++
			continue
		}
		j := i
		distinct := map[string]bool{}
		for j < len(lines) && isEnv[j] {
			distinct[names[j]] = true
			j++
		}
		if len(distinct) >= envDumpRun {
			for k := i; k < j; k++ {
				line, cr := strings.TrimSuffix(lines[k], "\r"), ""
				if line != lines[k] {
					cr = "\r"
				}
				m := envLinePattern.FindStringSubmatch(line)
				if m != nil && m[3] != "" && m[3] != placeholder {
					lines[k] = m[1] + placeholder + cr
					changed = true
				}
			}
		}
		i = j
	}
	if !changed {
		return s
	}
	return strings.Join(lines, "\n")
}

func isWordChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// scrubCommon is the part of the pipeline String and TraceText share.
func scrubCommon(s string) string {
	s = userinfoPattern.ReplaceAllString(s, "${1}"+placeholder)
	s = skPattern.ReplaceAllString(s, placeholder)
	s = githubTokenPattern.ReplaceAllString(s, placeholder)
	s = awsKeyPattern.ReplaceAllString(s, placeholder)
	s = stripeKeyPattern.ReplaceAllString(s, placeholder)
	s = jwtPattern.ReplaceAllString(s, placeholder)
	return s
}

// redactEmails applies emailPattern, but skips a match that looks like a
// remote-access target rather than an email address — the two are
// syntactically identical (local@domain.tld), and measured false
// positives covered both shapes context can rule out:
//
//   - immediately followed by ':' — git's SCP-like remote shorthand
//     (git@github.com:owner/repo.git) or an explicit port
//     (user@host:2222). A real email is never directly followed by a
//     colon in ordinary prose.
//   - immediately preceded by ssh/scp/rsync/sftp — "ssh deploy@host.com"
//     is a command, not contact information.
//
// Neither check is airtight (an email genuinely ending a sentence right
// before a colon, or "contact ssh@example.com", would slip through this
// heuristic in the direction of NOT redacting) — but the alternative measured
// on real assistant text was redacting git remotes and ssh targets wholesale,
// which is worse for a corpus this is trying to preserve.
func redactEmails(s string) string {
	locs := emailPattern.FindAllStringIndex(s, -1)
	if locs == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		if end < len(s) && s[end] == ':' {
			continue
		}
		if looksLikeRemoteTarget(s, start) {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(placeholder)
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

// looksLikeRemoteTarget reports whether s[:start] ends with a remote-access
// verb immediately adjacent to where a match starts (only spaces or tabs
// between) — "ssh deploy@..." qualifies, "contact ssh@..." does not,
// since "contact" sits between "ssh" and nothing there. It looks back from
// start and touches only those blanks and the verb: lowering and trimming
// all of s[:start] for every address is quadratic in a text of addresses.
func looksLikeRemoteTarget(s string, start int) bool {
	k := start
	for k > 0 && isBlank(s[k-1]) {
		k--
	}
	for _, verb := range remoteAccessVerbs {
		if k >= len(verb) && asciiEqualFold(s[k-len(verb):k], verb) {
			return true
		}
	}
	return false
}

// redactHomePaths applies homePathPattern and windowsHomePathPattern, but
// skips a Unix match immediately preceded by a domain-like character
// (letter, digit, or '.') — https://example.com/home/page measured as a
// false positive: "/home/" there is a URL path segment, not a filesystem
// path, and the character right before it ('m', part of ".com") is exactly
// what a real filesystem path never has immediately before it (a path
// starts at whitespace, a quote, '=', ':', or the beginning of the
// string). The Windows pattern needs no such check: "C:\Users\" does not
// occur as a URL path segment.
func redactHomePaths(s string) string {
	s = windowsHomePathPattern.ReplaceAllString(s, "${1}"+placeholder+"${2}")

	// Submatch indices, not just the whole-match indices: group 1 is
	// "/Users/" or "/home/", which the replacement keeps verbatim while
	// masking only what follows it (the username).
	matches := homePathPattern.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		prefixStart, prefixEnd := m[2], m[3]
		if start > 0 && isDomainChar(s[start-1]) {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(s[prefixStart:prefixEnd])
		b.WriteString(placeholder)
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

func isDomainChar(c byte) bool {
	return c == '.' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// Error returns an error whose text has been scrubbed. The original error is
// deliberately not wrapped: keeping it reachable via Unwrap would keep the
// unscrubbed text reachable too.
func Error(err error) error {
	if err == nil {
		return nil
	}
	return redactedError(String(err.Error()))
}

type redactedError string

func (e redactedError) Error() string { return string(e) }

// NewLogger builds the process logger: JSON to w, filtered at level, every
// record passing through the scrubbing handler.
func NewLogger(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(&handler{inner: slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})})
}

// NewStdLogger adapts the structured logger for net/http's two *log.Logger
// fields (http.Server.ErrorLog, httputil.ReverseProxy.ErrorLog). Both write
// pre-formatted lines that would otherwise bypass redaction entirely.
func NewStdLogger(logger *slog.Logger, level slog.Level) *log.Logger {
	return log.New(&slogWriter{logger: logger, level: level}, "", 0)
}

type slogWriter struct {
	logger *slog.Logger
	level  slog.Level
}

func (w *slogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSuffix(string(p), "\n")
	w.logger.Log(context.Background(), w.level, String(msg))
	return len(p), nil
}

type handler struct {
	inner slog.Handler
}

func (h *handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, String(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(scrubAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	scrubbed := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		scrubbed[i] = scrubAttr(a)
	}
	return &handler{inner: h.inner.WithAttrs(scrubbed)}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{inner: h.inner.WithGroup(name)}
}

func scrubAttr(a slog.Attr) slog.Attr {
	if _, deny := keyDenylist[strings.ToLower(a.Key)]; deny {
		return slog.String(a.Key, placeholder)
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		group := v.Group()
		members := make([]any, 0, len(group))
		for _, g := range group {
			members = append(members, scrubAttr(g))
		}
		return slog.Group(a.Key, members...)
	case slog.KindString:
		return slog.String(a.Key, String(v.String()))
	case slog.KindAny:
		return slog.String(a.Key, renderAny(v.Any()))
	default:
		return slog.Attr{Key: a.Key, Value: v}
	}
}

// renderAny refuses header maps and userinfo wholesale and scrubs everything
// else through its string form.
func renderAny(v any) string {
	switch t := v.(type) {
	case http.Header, *http.Header, *url.Userinfo:
		return placeholder
	case error:
		return String(t.Error())
	case fmt.Stringer:
		return String(t.String())
	default:
		return String(fmt.Sprint(v))
	}
}
