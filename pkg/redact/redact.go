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
	"os"
	"os/user"
	"regexp"
	"strings"
	"sync"
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
	// binaries; the Unix-only pattern above missed this entirely.
	windowsHomePathPattern = regexp.MustCompile(`(?i)([A-Z]:\\Users\\)[^\\\s]+`)
	// One line of an environment listing: optional `export `, a name, `=`,
	// and the rest of the line. Applied via redactEnvDumps, to runs only.
	envLinePattern = regexp.MustCompile(`^(\s*(?:export\s+)?[A-Za-z_][A-Za-z0-9_]*=)(.*)$`)
)

// envDumpRun is how many consecutive NAME=value lines make an environment
// dump. One or two such lines are ordinary prose about a setting; five in
// a row is the output of env or printenv quoted back, and nothing in it
// has a shape a pattern could pick the secrets out by.
const envDumpRun = 5

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
// It also covers what an assistant writes around a search that has no
// credential shape at all: an environment dump, a secret assigned by name,
// this client's own trace bridge, and the machine's hostname and account
// name wherever they stand. The log path does not need those: a log line
// is this client's own words, not a model's account of what it just read.
func TraceText(s string) string {
	s = scrubCommon(s)
	s = redactSecretAssignments(s)
	s = redactEnvDumps(s)
	s = redactEmails(s)
	s = redactHomePaths(s)
	s = redactLocalIdentity(s)
	return s
}

// redactEnvDumps replaces every value in a run of envDumpRun or more
// consecutive NAME=value lines. The names stay: they say what kind of
// output this was, and they identify nobody.
func redactEnvDumps(s string) string {
	if strings.Count(s, "=") < envDumpRun {
		return s
	}
	lines := strings.Split(s, "\n")
	isEnv := func(i int) bool { return envLinePattern.MatchString(strings.TrimSuffix(lines[i], "\r")) }
	changed := false
	for i := 0; i < len(lines); {
		if !isEnv(i) {
			i++
			continue
		}
		j := i
		for j < len(lines) && isEnv(j) {
			j++
		}
		if j-i >= envDumpRun {
			for k := i; k < j; k++ {
				line, cr := strings.TrimSuffix(lines[k], "\r"), ""
				if line != lines[k] {
					cr = "\r"
				}
				m := envLinePattern.FindStringSubmatch(line)
				if m[2] != "" && m[2] != placeholder {
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

// genericIdentityNames are account and host names that identify nobody
// and are ordinary words in a model's prose. Replacing every "user" or
// "admin" in a trajectory would destroy the text to hide nothing.
var genericIdentityNames = map[string]bool{
	"root": true, "user": true, "users": true, "admin": true, "administrator": true,
	"ubuntu": true, "debian": true, "runner": true, "guest": true, "test": true, "dev": true,
	"home": true, "node": true, "app": true, "www": true, "git": true, "deploy": true,
	"build": true, "docker": true, "vagrant": true, "localhost": true, "local": true,
	"server": true, "host": true, "macbook": true, "mac": true, "desktop": true,
	"laptop": true, "workstation": true, "default": true, "system": true, "nobody": true,
	"daemon": true, "code": true, "agent": true, "main": true, "master": true,
}

// identityNamePattern is what a name must look like to be searched for:
// ASCII, so the Go and JavaScript scrubbers fold case the same way.
var identityNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{3,}$`)

var (
	identityMu       sync.Mutex
	identityResolved bool
	identityPatterns []*regexp.Regexp
)

// SetLocalIdentity names the host and the account whose names TraceText
// removes, in place of asking the operating system, and returns a function
// that restores what was there. Tests use it so a guarantee about a name
// does not depend on the machine it runs on.
func SetLocalIdentity(host, account string) (restore func()) {
	identityMu.Lock()
	defer identityMu.Unlock()
	prevResolved, prevPatterns := identityResolved, identityPatterns
	identityResolved, identityPatterns = true, compileIdentity(host, account)
	return func() {
		identityMu.Lock()
		defer identityMu.Unlock()
		identityResolved, identityPatterns = prevResolved, prevPatterns
	}
}

// compileIdentity keeps the hostname's first label and the account name,
// each only when it is specific enough to identify this machine or person.
func compileIdentity(host, account string) []*regexp.Regexp {
	if i := strings.IndexByte(host, '.'); i >= 0 {
		host = host[:i]
	}
	// A Windows account arrives as DOMAIN\name; the name is what is typed.
	if i := strings.LastIndexByte(account, '\\'); i >= 0 {
		account = account[i+1:]
	}
	var out []*regexp.Regexp
	seen := map[string]bool{}
	for _, name := range []string{host, account} {
		lower := strings.ToLower(name)
		if !identityNamePattern.MatchString(name) || genericIdentityNames[lower] || seen[lower] {
			continue
		}
		seen[lower] = true
		out = append(out, regexp.MustCompile(`(?i)`+regexp.QuoteMeta(name)))
	}
	return out
}

func localIdentity() []*regexp.Regexp {
	identityMu.Lock()
	defer identityMu.Unlock()
	if !identityResolved {
		host, _ := os.Hostname()
		account := ""
		if u, err := user.Current(); err == nil {
			account = u.Username
		}
		identityResolved, identityPatterns = true, compileIdentity(host, account)
	}
	return identityPatterns
}

// redactLocalIdentity replaces the machine's hostname (its first label)
// and the account name where either stands as a whole word: `USER=name`,
// `ssh name@host`, a prompt, plain prose. redactHomePaths already takes
// the name out of /home/<name>; this is the same name everywhere else.
func redactLocalIdentity(s string) string {
	for _, re := range localIdentity() {
		if !re.MatchString(s) {
			continue
		}
		var b strings.Builder
		last, pos := 0, 0
		for pos < len(s) {
			loc := re.FindStringIndex(s[pos:])
			if loc == nil {
				break
			}
			start, end := pos+loc[0], pos+loc[1]
			if (start > 0 && isWordChar(s[start-1])) || (end < len(s) && isWordChar(s[end])) {
				// Not a whole word here. Look again one byte on rather than
				// past it, so an occurrence that overlaps this one is still
				// found, as a regular expression with lookarounds finds it.
				pos = start + 1
				continue
			}
			b.WriteString(s[last:start])
			b.WriteString(placeholder)
			last, pos = end, end
		}
		b.WriteString(s[last:])
		s = b.String()
	}
	return s
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
		if looksLikeRemoteTarget(s[:start]) {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(placeholder)
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

// looksLikeRemoteTarget reports whether before ends with a remote-access
// verb immediately adjacent to where a match starts (only whitespace
// between) — "ssh deploy@..." qualifies, "contact ssh@..." does not,
// since "contact" sits between "ssh" and nothing there.
func looksLikeRemoteTarget(before string) bool {
	trimmed := strings.ToLower(strings.TrimRight(before, " \t"))
	for _, verb := range remoteAccessVerbs {
		if strings.HasSuffix(trimmed, verb) {
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
	s = windowsHomePathPattern.ReplaceAllString(s, "${1}"+placeholder)

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
