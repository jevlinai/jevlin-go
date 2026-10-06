package redact

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// The machine's own names, removed from trace text only. The hostname's
// first label is removed wherever it stands as a whole word: a hostname is
// rarely a word of prose, and it identifies the machine wherever it is. The
// account name is removed only where the text uses it as an account, because
// an account is often a word (will, max, claude) and removing every one
// would destroy the prose to hide a name that, in prose, says nothing about
// whose machine this is:
//
//   - the value of an assignment to USER, USERNAME, LOGNAME or SUDO_USER
//     (any letter case, spaces around `=`, optionally quoted);
//   - immediately before `@`: ssh name@host, a prompt name@host:~$;
//   - after a home directory prefix: /home/, /Users/, C:\Users\, also
//     inside /mnt/c/Users/, and with a space in it (C:\Users\John Smith).
//
// cmd/jevlin/agent_trace_common.js implements the same rules; the shared
// table in testdata/trace_cases.json holds the two to the same bytes.

// genericIdentityNames are account and host names that identify nobody:
// ordinary words, and the names platforms, images and installers give
// every machine or account of their kind. Searching for one would rewrite
// every "user" or "api" in a trajectory to hide nothing. A macOS default
// name is generic only bare: "macbook-pro" is every Mac, while an owner's
// "alices-macbook-pro" is one.
var genericIdentityNames = map[string]bool{
	"root": true, "user": true, "users": true, "admin": true, "administrator": true,
	"ubuntu": true, "debian": true, "runner": true, "guest": true, "test": true, "dev": true,
	"home": true, "node": true, "app": true, "www": true, "git": true, "deploy": true,
	"build": true, "docker": true, "vagrant": true, "localhost": true, "local": true,
	"server": true, "host": true, "macbook": true, "mac": true, "desktop": true,
	"laptop": true, "workstation": true, "default": true, "system": true, "nobody": true,
	"daemon": true, "code": true, "agent": true, "main": true, "master": true,
	"vscode": true, "core": true, "codespace": true, "coder": true, "gitpod": true,
	"jovyan": true, "ec2-user": true, "azureuser": true, "jenkins": true, "circleci": true,
	"gitlab-runner": true, "runneradmin": true, "bun": true, "deno": true, "owner": true,
	"raspberrypi": true, "kali": true, "nixos": true, "penguin": true, "fedora": true,
	"archlinux": true, "api": true, "web": true, "prod": true, "staging": true, "worker": true,
	"macbook-pro": true, "macbook-air": true, "mac-mini": true, "imac": true, "mac-studio": true,
	// The placeholder's own word: searching for it would find every
	// [REDACTED] and wrap it again on each pass.
	"redacted": true,
}

// accountVariables are the variables whose value is an account name, in
// lower case; the match is ASCII case-insensitive.
var accountVariables = map[string]bool{"user": true, "username": true, "logname": true, "sudo_user": true}

// A name must look like this to be searched for: ASCII, so Go and
// JavaScript fold case the same way, and at least three characters. An
// account may hold single spaces, as a Windows display-style account does.
var (
	hostNamePattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{3,}$`)
	accountNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+(?: [A-Za-z0-9._-]+)*$`)
)

type localNames struct {
	host        *regexp.Regexp // the hostname's first label
	account     *regexp.Regexp // the account name
	accountHome *regexp.Regexp // a home directory prefix, then the account name
}

var (
	identityMu       sync.Mutex
	identityResolved bool
	identity         localNames
)

// SetLocalIdentity names the host and the account whose names TraceText
// removes, in place of asking the operating system, and returns a function
// that restores what was there. Tests use it so a guarantee about a name
// does not depend on the machine it runs on.
func SetLocalIdentity(host, account string) (restore func()) {
	identityMu.Lock()
	defer identityMu.Unlock()
	prevResolved, prev := identityResolved, identity
	identityResolved, identity = true, compileIdentity(host, account)
	return func() {
		identityMu.Lock()
		defer identityMu.Unlock()
		identityResolved, identity = prevResolved, prev
	}
}

// compileIdentity keeps the hostname's first label and the account name,
// each only when it is specific enough to identify this machine or person.
func compileIdentity(host, account string) localNames {
	if i := strings.IndexByte(host, '.'); i >= 0 {
		host = host[:i]
	}
	// A Windows account arrives as DOMAIN\name; the name is what is typed.
	if i := strings.LastIndexByte(account, '\\'); i >= 0 {
		account = account[i+1:]
	}
	var out localNames
	if hostNamePattern.MatchString(host) && !genericIdentityNames[strings.ToLower(host)] {
		out.host = regexp.MustCompile(`(?i)` + regexp.QuoteMeta(host))
	}
	if len(account) >= 3 && accountNamePattern.MatchString(account) && !genericIdentityNames[strings.ToLower(account)] {
		quoted := regexp.QuoteMeta(account)
		out.account = regexp.MustCompile(`(?i)` + quoted)
		out.accountHome = regexp.MustCompile(`(/Users/|/home/|(?i:[A-Z]:\\+Users\\+))(?i:` + quoted + `)`)
	}
	return out
}

func localIdentity() localNames {
	identityMu.Lock()
	defer identityMu.Unlock()
	if !identityResolved {
		host, _ := os.Hostname()
		identityResolved, identity = true, compileIdentity(host, localAccount(runtime.GOOS, os.Getenv, os.UserHomeDir))
	}
	return identity
}

// localAccount is the account name, found the same way the JavaScript
// scrubber finds it and with nothing that can block: USERNAME on Windows,
// USER and then LOGNAME elsewhere, and failing those the last element of
// the home directory. The user database is not asked. os/user's lookup can
// wait on a domain controller a domain-joined Windows machine cannot reach,
// fails under a macOS sandbox, and without cgo (how releases are built)
// falls back to $USER anyway; Node's os.userInfo() reads a different source
// again. The environment and the home directory are what both languages
// read the same way.
func localAccount(goos string, getenv func(string) string, home func() (string, error)) string {
	names := []string{"USER", "LOGNAME"}
	if goos == "windows" {
		names = []string{"USERNAME"}
	}
	for _, name := range names {
		if v := getenv(name); v != "" {
			return v
		}
	}
	if dir, err := home(); err == nil && dir != "" {
		return filepath.Base(dir)
	}
	return ""
}

// redactLocalIdentity replaces the hostname's first label wherever it
// stands as a whole word, then the account name where namesAnAccount says
// the text uses it as an account.
func redactLocalIdentity(s string) string {
	id := localIdentity()
	if id.host != nil {
		s = replaceWholeWords(s, id.host, nil)
	}
	if id.account != nil {
		s = replaceWholeWords(s, id.account, namesAnAccount)
	}
	return s
}

// replaceWholeWords replaces each match of re that stands as a whole word
// and that accept, when given, accepts.
func replaceWholeWords(s string, re *regexp.Regexp, accept func(s string, start, end int) bool) string {
	if !re.MatchString(s) {
		return s
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
		if accept != nil && !accept(s, start, end) {
			pos = end
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(placeholder)
		last, pos = end, end
	}
	b.WriteString(s[last:])
	return b.String()
}

// namesAnAccount reports whether the name at s[start:end] is used as an
// account: directly before `@`, or as the value of an assignment to one of
// accountVariables.
func namesAnAccount(s string, start, end int) bool {
	if end < len(s) && s[end] == '@' {
		return true
	}
	p := start
	if p > 0 && (s[p-1] == '"' || s[p-1] == '\'') {
		p--
	}
	for p > 0 && isBlank(s[p-1]) {
		p--
	}
	if p == 0 || s[p-1] != '=' {
		return false
	}
	p--
	for p > 0 && isBlank(s[p-1]) {
		p--
	}
	q := p
	for q > 0 && isWordChar(s[q-1]) {
		q--
	}
	return accountVariables[strings.ToLower(s[q:p])]
}

// redactAccountHomes replaces the account name after a home directory
// prefix where it ends the path segment. It runs before redactHomePaths,
// whose segment stops at whitespace, so an account with a space in it is
// removed whole; and it does not ask what stands before the prefix, so the
// account inside /mnt/c/Users/ is removed too, which redactHomePaths leaves
// as a possible URL path.
func redactAccountHomes(s string) string {
	re := localIdentity().accountHome
	if re == nil || !re.MatchString(s) {
		return s
	}
	var b strings.Builder
	last, pos := 0, 0
	for pos < len(s) {
		m := re.FindStringSubmatchIndex(s[pos:])
		if m == nil {
			break
		}
		start, end, prefixEnd := pos+m[0], pos+m[1], pos+m[3]
		if end < len(s) && isAccountChar(s[end]) {
			pos = start + 1
			continue
		}
		b.WriteString(s[last:prefixEnd])
		b.WriteString(placeholder)
		last, pos = end, end
	}
	b.WriteString(s[last:])
	return b.String()
}

func isAccountChar(c byte) bool { return isNameChar(c) }
