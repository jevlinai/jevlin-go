// jevlin: shared trace preparation for the JavaScript hosts.
//
// ONE source of truth. Every host whose lineage channel is a JavaScript or
// TypeScript artifact we install — opencode's in-process plugin, Pi's
// auto-discovered extension — is RENDERED from its own template with this
// file spliced in where that template carries the trace-common marker
// (renderAgentScript in agents.go). The installed files stay standalone,
// with no import of ours to resolve at runtime and no package manager in
// the picture, but the scrub and the caps below exist exactly once in the
// repository.
//
// That is the point. A second, weaker copy of this logic in a second
// adapter is the failure this file exists to prevent: the adapter is what
// builds the bridge, so an adapter that scrubs late — or not at all — puts
// raw assistant text into a process argument before the Go binary ever
// sees it. The order here is not negotiable:
//
//	complete bounded source -> scrub -> UTF-8 byte accounting ->
//	32 KiB history tail -> envelope -> 48 KiB envelope check -> base64url
//
// Never truncate before scrubbing: a slice can cut a secret in half and
// leave both halves unrecognizable to the redactor. An entry too large to
// scrub whole is omitted whole instead.
//
// Everything here fails open. A host adapter that throws leaves the tool
// call exactly as it found it and the search runs with its per-shell
// trace, which is the whole fallback contract: tracing must never be the
// reason a search does not run.
import { createHash } from "node:crypto"
import { homedir as traceHomedir, hostname as traceHostname } from "node:os"
import { basename as traceBasename } from "node:path"

// The trace hash: domain-separated SHA-256 over a PUBLIC prefix, truncated
// to 16 bytes and hex-encoded — the same derivation as traceHash in
// trace.go, so a host id hashed here and one hashed in Go agree. It is not
// keyed and is not a secret: it exists so a stable identifier can leave the
// machine without the host's own id leaving with it.
const TRACE_PREFIX = "tokendrop-trace-v1|"
// The visible-text cap: what may ride as history, after scrubbing.
const TRACE_HISTORY_CAP = 32 * 1024
// The complete-source budget: an entry larger than this is omitted whole
// rather than sliced, so redaction always sees a complete entry. Matches
// hookTailBytes in hook.go, which prepareTraceText applies on the Go side.
const TRACE_SOURCE_CAP = 256 * 1024
// The whole-envelope cap (compact JSON). Above it history is dropped
// first; an envelope still above it is dropped whole. A trace must never
// be the reason a search fails, and must never push a valid search
// invocation past the host's command-length limit.
const TRACE_ENVELOPE_CAP = 48 * 1024

// Our search command, by bare name or any path, optionally quoted,
// optionally .exe. Pinned by test to searchCommandRe in hook.go — the one
// recognizer every host goes through — so no adapter can quietly loosen
// into a substring match. This is lineage-injection detection: it decides
// whether we thread a trace onto a command the host has ALREADY decided to
// run. It is not an authorization decision and must never become one.
const SEARCH_RE = /(?:^|[\s;&|(]|\$\()\s*(?:&\s*)?(?:[A-Za-z]:)?["']?(?:[^\s"']*[\\/])?jevlin(?:\.exe)?["']?\s+search(?:\s|$)/

// SEARCH_LEADS_RE anchors SEARCH_RE's invocation: the search is the first
// simple command of the line, leading plain NAME=value assignments aside —
// the only place a POSIX assignment prefix reaches. One rule with
// posixSearchLeadsRe in bridge.go, pinned by TestBridgeGuardsAgree and
// TestTheLeadsGuardsAreOneRegex.
const SEARCH_LEADS_RE = /^\s*(?:[A-Za-z_][A-Za-z0-9_]*=[^\s"'\\]*\s+)*(?:[A-Za-z]:)?["']?(?:[^\s"']*[\\/])?jevlin(?:\.exe)?["']?\s+search(?:\s|$)/

const TRACE_BRIDGE_ENV = "JEVLIN_TRACE_BRIDGE"

const traceHash = (raw) => createHash("sha256").update(TRACE_PREFIX + raw).digest("hex").slice(0, 32)

// Mirror pkg/redact.TraceText for complete source entries before the bridge
// is capped. The Go consumer applies its own preparation again. Every step
// below is one function in pkg/redact, in the same order, and the shared
// table in pkg/redact/testdata/trace_cases.json and
// TestSharedTraceSourceRedactionBoundaries hold the two to the same bytes.
//
// The character classes are written out ([\t\n\f\r ] rather than \s, [^\n]
// rather than .) because JavaScript's shorthands are wider than Go's, and a
// rule that fires in one language and not the other is a parity bug. For
// the same reason no pattern here uses the i flag: Go's (?i) folds k with
// U+212A KELVIN SIGN and s with U+017F LONG S, JavaScript's i without u
// folds neither, and with u its \w-style classes fold them too. Where Go
// folds case, the class is written out letter by letter (traceFold).
const TRACE_REDACTED = '[REDACTED]'

// traceFold writes an ASCII name as a pattern that matches what Go's (?i)
// matches for it: each letter in either case, plus the two non-ASCII
// letters Go folds into ASCII ones, and a dot as a dot.
const traceFold = (name) => name.replace(/[A-Za-z.]/g, (c) => {
  if (c === '.') return '\\.'
  const l = c.toLowerCase()
  return '[' + l + c.toUpperCase() + (l === 'k' ? '\u212A' : l === 's' ? '\u017F' : '') + ']'
})

// The end of the blanks (space, tab) that close s[from:to]: a loop, because
// /[ \t]+$/ restarts at every blank of a run that something else ends and
// takes time quadratic in the run.
const traceTrimBlanksEnd = (s, from, to) => {
  while (to > from && traceIsBlank(s.charCodeAt(to - 1))) to--
  return to
}

// A bare JWT, as Go's \beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\b
// matches it, by a scan rather than that expression. A backtracking engine
// restarts it at every eyJ that follows a `-` and runs each start to the end
// of the same segment, which is quadratic in a run such as -eyJ-eyJ-eyJ….
// Every start inside one run of [A-Za-z0-9_-] shares that run's end, and the
// first segment must be the whole rest of the run (the next character has to
// be a dot), so the leftmost start decides for all of them: when it fails, the
// scan moves past the run. The third segment ends at the last word character
// of its run, where \b holds; a trailing `-` is not a word character.
const traceIsJwtChar = (c) => traceIsWord(c) || c === 45
const traceJwtRunEnd = (s, i) => {
  while (i < s.length && traceIsJwtChar(s.charCodeAt(i))) i++
  return i
}
const redactTraceJwts = (text) => {
  let out = ''
  let last = 0
  let i = text.indexOf('eyJ')
  while (i >= 0) {
    if (i > 0 && traceIsWord(text.charCodeAt(i - 1))) {
      i = text.indexOf('eyJ', i + 1)
      continue
    }
    const r1 = traceJwtRunEnd(text, i + 3)
    let end = -1
    if (r1 - (i + 3) >= 4 && text.charCodeAt(r1) === 46) {
      const r2 = traceJwtRunEnd(text, r1 + 1)
      if (r2 - (r1 + 1) >= 4 && text.charCodeAt(r2) === 46) {
        let r3 = traceJwtRunEnd(text, r2 + 1)
        while (r3 > r2 + 1 && text.charCodeAt(r3 - 1) === 45) r3--
        if (r3 - (r2 + 1) >= 4) end = r3
      }
    }
    if (end < 0) {
      i = text.indexOf('eyJ', r1)
      continue
    }
    out += text.slice(last, i) + TRACE_REDACTED
    last = end
    i = text.indexOf('eyJ', end)
  }
  return last === 0 ? text : out + text.slice(last)
}

// scrubCommon: credentials with a recognizable shape.
// Start URL/email scans at token boundaries to avoid rescanning long words.
const scrubTraceCommon = (text) => redactTraceJwts(text
  .replace(/(?<![a-zA-Z0-9+.-])([a-zA-Z0-9+.-]*:\/\/)[^/@\t\n\f\r ]+@/g, (match, prefix) =>
    /[a-zA-Z]/.test(prefix) ? prefix + TRACE_REDACTED : match)
  .replace(/\b(?:sk|sr)-[A-Za-z0-9_-]{16,}/g, TRACE_REDACTED)
  .replace(/\bgh[opsur]_[A-Za-z0-9]{20,}\b|\bgithub_pat_[A-Za-z0-9_]{20,}\b/g, TRACE_REDACTED)
  .replace(/\bAKIA[0-9A-Z]{16}\b/g, TRACE_REDACTED))

// redactSecretAssignments: the value of NAME=value when a whole segment of
// NAME (split on _ - .) is a secret word, or NAME is our own trace bridge,
// whose value is an envelope no pattern can see into. KEY and PASS count
// only in a name with no lowercase letter or of two or more segments:
// `key=value` and `pass=2` are prose. A scanner, not one regular
// expression, so a name that is not a secret's consumes nothing and a
// secret chained after it is still read: every rule here is the one in
// pkg/redact/assign.go, decided over ASCII code units.
const TRACE_SECRET_SEGMENTS = new Set(['PASSWORD', 'PASSWD', 'PASSPHRASE', 'PGPASSWORD',
  'SECRET', 'SECRETS', 'TOKEN', 'CREDENTIAL', 'CREDENTIALS', 'APIKEY'])
const TRACE_SECRET_SEGMENTS_QUALIFIED = new Set(['KEY', 'PASS'])
const TRACE_SECRET_NAMES = new Set(['MYSQL_PWD'])
const TRACE_QUOTED_VALUE_MAX_LINES = 100
const traceSecretName = (name) => {
  const upper = name.toUpperCase()
  if (TRACE_SECRET_NAMES.has(upper)) return true
  const segs = upper.split(/[_.-]/).filter((seg) => seg !== '')
  const qualified = name === upper || segs.length >= 2
  return segs.some((seg) => TRACE_SECRET_SEGMENTS.has(seg) || (qualified && TRACE_SECRET_SEGMENTS_QUALIFIED.has(seg)))
}
const traceIsWord = (c) => c === 95 || (c >= 48 && c <= 57) || (c >= 65 && c <= 90) || (c >= 97 && c <= 122)
const traceIsNameStart = (c) => c === 95 || (c >= 65 && c <= 90) || (c >= 97 && c <= 122)
const traceIsName = (c) => traceIsWord(c) || c === 45 || c === 46
const traceIsBlank = (c) => c === 32 || c === 9
const traceIsSpace = (c) => c === 32 || c === 9 || c === 10 || c === 12 || c === 13
// The quote at v closes at the returned index's left, or -1: not within
// TRACE_QUOTED_VALUE_MAX_LINES line breaks. A backslash escapes one unit.
const traceClosingQuote = (s, v) => {
  const q = s.charCodeAt(v)
  let lines = 0
  for (let i = v + 1; i < s.length; i++) {
    let c = s.charCodeAt(i)
    if (c === 92) {
      i++
      if (i >= s.length) break
      c = s.charCodeAt(i)
    } else if (c === q) {
      return i + 1
    }
    if (c === 10 && ++lines > TRACE_QUOTED_VALUE_MAX_LINES) return -1
  }
  return -1
}
const traceLineEnd = (s, v) => {
  const i = s.indexOf('\n', v)
  if (i < 0) return s.length
  return i > v && s.charCodeAt(i - 1) === 13 ? i - 1 : i
}
const traceRunEnd = (s, i) => {
  while (i < s.length) {
    const c = s.charCodeAt(i)
    if (traceIsSpace(c) || c === 38 || c === 59) break
    i++
  }
  return i
}
// Hand back a trailing , and any trailing ) ] } or quote the value has
// more of than it opened: they close something the value sits inside.
const traceTrimValueTail = (s, from, to) => {
  let paren = 0, bracket = 0, brace = 0, dquote = 0, squote = 0
  for (let i = from; i < to; i++) {
    switch (s.charCodeAt(i)) {
      case 40: paren--; break
      case 41: paren++; break
      case 91: bracket--; break
      case 93: bracket++; break
      case 123: brace--; break
      case 125: brace++; break
      case 34: dquote++; break
      case 39: squote++; break
    }
  }
  while (to > from) {
    const c = s.charCodeAt(to - 1)
    if (c === 44) { /* , */ }
    else if (c === 41 && paren > 0) paren--
    else if (c === 93 && bracket > 0) bracket--
    else if (c === 125 && brace > 0) brace--
    else if (c === 34 && dquote % 2 === 1) dquote--
    else if (c === 39 && squote % 2 === 1) squote--
    else return to
    to--
  }
  return to
}
const traceSecretValueEnd = (s, v) => {
  if (v >= s.length) return v
  const c = s.charCodeAt(v)
  if (c === 34 || c === 39) {
    const closed = traceClosingQuote(s, v)
    return closed < 0 ? traceLineEnd(s, v) : traceTrimValueTail(s, closed, traceRunEnd(s, closed))
  }
  if (c === 61) return v
  return traceTrimValueTail(s, v, traceRunEnd(s, v))
}
// The secret value behind the = at e, as [start, end], or null. The name is
// read backwards from e and never past floor, the end of the last value
// removed.
const traceSecretValueAt = (s, floor, e) => {
  let k = e
  while (k > floor && traceIsBlank(s.charCodeAt(k - 1))) k--
  let r = k
  while (r > floor && traceIsName(s.charCodeAt(r - 1))) r--
  if (r === k || !traceIsWord(s.charCodeAt(k - 1))) return null
  let p = -1
  for (let q = r; q < k; q++) {
    if (traceIsNameStart(s.charCodeAt(q)) && (q === 0 || !traceIsWord(s.charCodeAt(q - 1)))) { p = q; break }
  }
  if (p < 0) return null
  const name = s.slice(p, k)
  const psEnv = p >= 5 && /^\$[Ee][Nn][Vv]:$/.test(s.slice(p - 5, p))
  // The bridge's name alone or as the last part of a dotted or hyphenated
  // name: a later step can remove what stands before it (an email's domain),
  // and a second pass must not then find what the first did not.
  const upperName = name.toUpperCase()
  const bridge = upperName === TRACE_BRIDGE_ENV || upperName.endsWith('.' + TRACE_BRIDGE_ENV) || upperName.endsWith('-' + TRACE_BRIDGE_ENV)
  if (k < e && !psEnv && !bridge) return null
  if (!bridge && !traceSecretName(name)) return null
  let v = e + 1
  if (psEnv || bridge) while (v < s.length && traceIsBlank(s.charCodeAt(v))) v++
  const end = traceSecretValueEnd(s, v)
  if (end === v || s.slice(v, end) === TRACE_REDACTED) return null
  return [v, end]
}
const redactTraceSecretAssignments = (text) => {
  let e = text.indexOf('=')
  if (e < 0) return text
  let out = ''
  let last = 0
  let changed = false
  while (e >= 0) {
    let next = e + 1
    const found = traceSecretValueAt(text, last, e)
    if (found) {
      out += text.slice(last, found[0]) + TRACE_REDACTED
      last = next = found[1]
      changed = true
    }
    e = text.indexOf('=', next)
  }
  return changed ? out + text.slice(last) : text
}

// redactEnvDumps: every value in a run of consecutive environment lines
// naming at least five distinct variables: the output of env, printenv or
// `declare -x`, quoted back, where nothing has a shape a pattern could pick
// secrets out by. A line may carry a list marker or a line number. A line
// whose value starts with `=`, ends with `,` or carries another NAME= after
// whitespace is code or a log record, and breaks the run.
const TRACE_ENV_DUMP_RUN = 5
const TRACE_ENV_MARKER = '(?:(?:[-*+>]|[0-9]+[.)]?)[\\t ]+)?'
const TRACE_ENV_LINE = new RegExp('^([\\t\\n\\f\\r ]*' + TRACE_ENV_MARKER + '(?:(?:export|(?:declare|typeset)[\\t ]+-[A-Za-z]+)[\\t ]+)?([A-Za-z_][A-Za-z0-9_]*)=)([^\\n]*)$')
const TRACE_ENV_BARE_DECLARE = new RegExp('^[\\t\\n\\f\\r ]*' + TRACE_ENV_MARKER + '(?:declare|typeset)[\\t ]+-[A-Za-z]+[\\t ]+([A-Za-z_][A-Za-z0-9_]*)[\\t\\n\\f\\r ]*$')
const TRACE_ENV_PAIR_AFTER_SPACE = /[\t\n\f\r ][A-Za-z_][A-Za-z0-9_]*=/
const redactTraceEnvDumps = (text) => {
  if (!text.includes('=')) return text
  const lines = text.split('\n')
  const bare = (i) => lines[i].endsWith('\r') ? lines[i].slice(0, -1) : lines[i]
  const names = []
  const isEnv = []
  for (let i = 0; i < lines.length; i++) {
    const line = bare(i)
    const m = TRACE_ENV_LINE.exec(line)
    if (m) {
      const v = m[3]
      const vEnd = traceTrimBlanksEnd(v, 0, v.length)
      isEnv[i] = !v.startsWith('=') && !(vEnd > 0 && v.charCodeAt(vEnd - 1) === 44) && !TRACE_ENV_PAIR_AFTER_SPACE.test(v)
      names[i] = m[2]
      continue
    }
    const d = TRACE_ENV_BARE_DECLARE.exec(line)
    isEnv[i] = d !== null
    names[i] = d ? d[1] : ''
  }
  let changed = false
  for (let i = 0; i < lines.length;) {
    if (!isEnv[i]) { i++; continue }
    let j = i
    const distinct = new Set()
    while (j < lines.length && isEnv[j]) distinct.add(names[j++])
    if (distinct.size >= TRACE_ENV_DUMP_RUN) {
      for (let k = i; k < j; k++) {
        const line = bare(k)
        const cr = line === lines[k] ? '' : '\r'
        const m = TRACE_ENV_LINE.exec(line)
        if (m && m[3] !== '' && m[3] !== TRACE_REDACTED) { lines[k] = m[1] + TRACE_REDACTED + cr; changed = true }
      }
    }
    i = j
  }
  return changed ? lines.join('\n') : text
}

// One known difference from Go, left as it is: an address directly followed
// by one of . % + - and a second address ("bob@example.com.a1@example.org").
// Go's \b lets the second local part start at that punctuation, right where
// the first match ended, and removes ".a1@example.org"; the lookbehind here,
// which keeps this scan from restarting inside every long word, does not,
// and keeps it. No byte-identical rewrite is known that stays linear.
//
// The remote-access verb is looked for directly before the address, past
// any blanks, without copying or rescanning the text before it: once per
// address over everything before it is quadratic in a text of addresses.
const TRACE_REMOTE_ACCESS_VERBS = ['ssh', 'scp', 'rsync', 'sftp']
const traceLooksLikeRemoteTarget = (s, start) => {
  const k = traceTrimBlanksEnd(s, 0, start)
  return TRACE_REMOTE_ACCESS_VERBS.some((verb) => k >= verb.length && s.slice(k - verb.length, k).toLowerCase() === verb)
}
const redactTraceEmails = (text) => text
  .replace(/(?<![A-Za-z0-9._%+-])([.%+-]*)([A-Za-z0-9_][A-Za-z0-9._%+-]*@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b)/g, (match, leading, email, offset, source) =>
    source[offset + match.length] === ':' || traceLooksLikeRemoteTarget(source, offset + leading.length) ? match : leading + TRACE_REDACTED)

const redactTraceHomePaths = (text) => text
  .replace(/([A-Za-z\u212A\u017F]:\\[Uu][Ss\u017F][Ee][Rr][Ss\u017F]\\)[^\\\t\n\f\r ]+/g, '$1' + TRACE_REDACTED)
  .replace(/(\/Users\/|\/home\/)[^/\t\n\f\r ]+/g, (match, prefix, offset, source) =>
    offset > 0 && /[A-Za-z0-9.]/.test(source[offset - 1]) ? match : prefix + TRACE_REDACTED)

// The local identity, as pkg/redact/identity.go has it. The hostname's
// first label goes wherever it stands as a whole word. The account name goes
// only where the text uses it as an account, because an account is often a
// word (will, max, claude): as the value of USER, USERNAME, LOGNAME or
// SUDO_USER; directly before `@` (ssh name@host, a prompt); and after a home
// directory prefix, /mnt/c/Users/ and a name with a space included. A
// generic name identifies nobody and is not searched for, nor is one under
// three characters or with anything but ASCII in it, so both languages fold
// case the same way.
const TRACE_GENERIC_IDENTITY = new Set(['root', 'user', 'users', 'admin', 'administrator',
  'ubuntu', 'debian', 'runner', 'guest', 'test', 'dev', 'home', 'node', 'app', 'www', 'git', 'deploy',
  'build', 'docker', 'vagrant', 'localhost', 'local', 'server', 'host', 'macbook', 'mac', 'desktop',
  'laptop', 'workstation', 'default', 'system', 'nobody', 'daemon', 'code', 'agent', 'main', 'master',
  'vscode', 'core', 'codespace', 'coder', 'gitpod', 'jovyan', 'ec2-user', 'azureuser', 'jenkins', 'circleci',
  'gitlab-runner', 'runneradmin', 'bun', 'deno', 'owner', 'raspberrypi', 'kali', 'nixos', 'penguin', 'fedora',
  'archlinux', 'api', 'web', 'prod', 'staging', 'worker',
  'macbook-pro', 'macbook-air', 'mac-mini', 'imac', 'mac-studio', 'redacted'])
const TRACE_ACCOUNT_VARIABLES = new Set(['user', 'username', 'logname', 'sudo_user'])
const traceIdentityPatterns = (host, account) => {
  host = String(host || '')
  account = String(account || '')
  if (host.includes('.')) host = host.slice(0, host.indexOf('.'))
  if (account.includes('\\')) account = account.slice(account.lastIndexOf('\\') + 1)
  const out = { host: null, account: null, accountHome: null }
  if (/^[A-Za-z0-9._-]{3,}$/.test(host) && !TRACE_GENERIC_IDENTITY.has(host.toLowerCase())) {
    out.host = new RegExp('(?<![A-Za-z0-9_])' + traceFold(host) + '(?![A-Za-z0-9_])', 'g')
  }
  if (account.length >= 3 && /^[A-Za-z0-9._-]+(?: [A-Za-z0-9._-]+)*$/.test(account) && !TRACE_GENERIC_IDENTITY.has(account.toLowerCase())) {
    out.account = new RegExp('(?<![A-Za-z0-9_])' + traceFold(account) + '(?![A-Za-z0-9_])', 'g')
    out.accountHome = new RegExp('(\\/Users\\/|\\/home\\/|[A-Za-z\\u212A\\u017F]:\\\\[Uu][Ss\\u017F][Ee][Rr][Ss\\u017F]\\\\)' +
      traceFold(account) + '(?![A-Za-z0-9_.-])', 'g')
  }
  return out
}
// The account name, found the way pkg/redact's localAccount finds it and
// with nothing that can block: USERNAME on Windows, USER then LOGNAME
// elsewhere, then the last element of the home directory. os.userInfo()
// is not asked: it reads the user database, which Go does not.
const traceLocalAccount = () => {
  const env = process.env
  const named = process.platform === 'win32' ? env.USERNAME : (env.USER || env.LOGNAME)
  if (named) return named
  try { return traceBasename(traceHomedir()) } catch { return '' }
}
const TRACE_LOCAL_IDENTITY = (() => {
  let host = ''
  let account = ''
  try { host = traceHostname() } catch {}
  try { account = traceLocalAccount() } catch {}
  return traceIdentityPatterns(host, account)
})()
// The account is used as one: directly before `@`, or as the value of an
// assignment to an account variable, optionally quoted, spaces around `=`.
const traceNamesAnAccount = (s, start, end) => {
  if (s.charCodeAt(end) === 64) return true
  let p = start
  if (p > 0 && (s.charCodeAt(p - 1) === 34 || s.charCodeAt(p - 1) === 39)) p--
  while (p > 0 && traceIsBlank(s.charCodeAt(p - 1))) p--
  if (p === 0 || s.charCodeAt(p - 1) !== 61) return false
  p--
  while (p > 0 && traceIsBlank(s.charCodeAt(p - 1))) p--
  let q = p
  while (q > 0 && traceIsWord(s.charCodeAt(q - 1))) q--
  return TRACE_ACCOUNT_VARIABLES.has(s.slice(q, p).toLowerCase())
}
// redactAccountHomes: the account after a home directory prefix, before
// redactTraceHomePaths cuts the segment at a space.
const redactTraceAccountHomes = (text, identity) =>
  identity.accountHome ? text.replace(identity.accountHome, (match, prefix) => prefix + TRACE_REDACTED) : text
const redactTraceLocalIdentity = (text, identity) => {
  if (identity.host) text = text.replace(identity.host, TRACE_REDACTED)
  if (identity.account) {
    text = text.replace(identity.account, (match, offset, source) =>
      traceNamesAnAccount(source, offset, offset + match.length) ? TRACE_REDACTED : match)
  }
  return text
}

const scrubTraceText = (text, identity = TRACE_LOCAL_IDENTITY) =>
  redactTraceLocalIdentity(redactTraceHomePaths(redactTraceAccountHomes(redactTraceEmails(redactTraceEnvDumps(redactTraceSecretAssignments(scrubTraceCommon(text)))), identity)), identity)

// prepareTraceHistory takes the COMPLETE text parts of one assistant
// message — `{type: 'text', text}` entries, the shape opencode's message
// parts already have and the shape every other adapter converts to — and
// returns the scrubbed, capped tail that may ride as history.
//
// Returns null when the complete source is over budget: the entry is
// omitted whole, never sliced to fit, because slicing would hand the
// scrubber a severed secret. Returns "" when there is nothing to send.
const prepareTraceHistory = (parts, identity = TRACE_LOCAL_IDENTITY) => {
  const texts = []
  let size = 0
  for (const part of parts) {
    if (part?.type !== 'text' || typeof part.text !== 'string') continue
    size += Buffer.byteLength(part.text) + (texts.length ? 1 : 0)
    if (size > TRACE_SOURCE_CAP) return null
    texts.push(part.text)
  }
  const bytes = Buffer.from(scrubTraceText(texts.join('\n'), identity))
  // The tail, not the head: the words nearest the search are the ones that
  // explain it. Walk forward off any continuation byte so the cut lands on
  // a rune boundary and the result is still valid UTF-8.
  let start = Math.max(0, bytes.length - TRACE_HISTORY_CAP)
  while (start < bytes.length && (bytes[start] & 0xc0) === 0x80) start++
  return bytes.subarray(start).toString('utf8')
}

// traceBridge serializes the envelope and enforces the envelope cap:
// history is dropped first, and an envelope still too large yields null —
// no bridge at all, rather than a command the host cannot run. Returns the
// base64url bridge value.
const traceBridge = (env) => {
  if (Buffer.byteLength(JSON.stringify(env)) > TRACE_ENVELOPE_CAP) delete env.history
  if (Buffer.byteLength(JSON.stringify(env)) > TRACE_ENVELOPE_CAP) return null
  return Buffer.from(JSON.stringify(env)).toString("base64url")
}

// ── the bridge this adapter puts on the command ─────────────────────────
//
// H-R4: only a bridge THIS adapter generated for THIS call may carry this
// adapter's harness. So an adapter never stands down because a bridge is
// already there — a model can write one itself, and dropin-miner#68 is what that looks
// like at the router: a trace the model assembled, attributed to us. It
// removes every assignment it RECOGNIZES and prepends its own.
//
// "Recognized" is a syntactically standalone assignment statement in a
// declared shell's syntax, and nothing else: a leading NAME=value word in
// POSIX, a standalone `$env:NAME = …` statement in PowerShell, `set NAME=…`
// as its own command in cmd. Never a substring inside quoted text, inside
// another argument, or inside the JSON request body — a query that mentions
// the variable is still just a query. An assignment somewhere this cannot
// prove is standalone leaves the command untouched and no lineage claimed.
//
// The binary cannot authenticate an environment variable, so the client
// trace stays unauthenticated metadata either way; what this protects is
// the meaning of the harness field, not the trust in it.

// A standalone bridge assignment at the START of the command, in each
// declared shell's syntax, with the rest of the command after it.
const BRIDGE_ASSIGNMENT_RE = new RegExp(
  "^(?:" +
    // POSIX: NAME=value as a leading word.
    TRACE_BRIDGE_ENV + "=[^\\s]*\\s+" +
    "|" +
    // PowerShell: $env:NAME = '…' or "…" or a bare word, as its own
    // statement, ended by a newline or a semicolon.
    "\\$env:" + TRACE_BRIDGE_ENV + "\\s*=\\s*(?:'[^']*'|\"[^\"]*\"|[^\\s;]*)\\s*[;\\n]\\s*" +
    "|" +
    // cmd: set NAME=value as its own command.
    "set\\s+" + TRACE_BRIDGE_ENV + "=[^&\\n]*(?:&+|\\n)\\s*" +
    ")",
  "i",
)

// The try/finally wrapper this adapter writes around a PowerShell command,
// so one it has already rewritten is unwrapped rather than wrapped twice.
const PS_WRAPPER_RE = new RegExp(
  "^try \\{ ([\\s\\S]*)\\n\\} finally \\{ Remove-Item Env:" + TRACE_BRIDGE_ENV + "[^}]*\\}$",
)

// stripBridgeAssignments removes every bridge assignment it can prove is a
// standalone statement at the front of the command, and unwraps this
// adapter's own PowerShell wrapper.
const stripBridgeAssignments = (cmd) => {
  let rest = cmd
  for (;;) {
    const match = BRIDGE_ASSIGNMENT_RE.exec(rest)
    if (match) {
      rest = rest.slice(match[0].length)
      continue
    }
    const wrapped = PS_WRAPPER_RE.exec(rest)
    if (wrapped) {
      rest = wrapped[1]
      continue
    }
    return rest
  }
}

// carriesUnremovableBridge: the command still mentions the variable in a
// position this cannot prove is a standalone assignment. The adapter leaves
// such a command exactly as it found it.
const carriesUnremovableBridge = (cmd) => cmd.includes(TRACE_BRIDGE_ENV + "=") || cmd.includes(TRACE_BRIDGE_ENV + " =")

// needsTraceBridge: our search command. A bridge already on it is not a
// reason to stand down (H-R4); it is a reason to remove it first.
// Recognition only — the host has already decided to run this command.
const needsTraceBridge = (cmd) => typeof cmd === "string" && SEARCH_RE.test(cmd)

// withTraceBridge returns the command carrying THIS adapter's bridge, in the
// syntax of the shell that will run it, or null when the command must be
// left alone.
const withTraceBridge = (cmd, bridge, shell) => {
  const stripped = stripBridgeAssignments(cmd)
  if (carriesUnremovableBridge(stripped)) return null
  if (shell === "powershell") {
    // Assignment, then removal in a finally. A `& { … }` block looks like it
    // would scope this and does not: $env: is the process environment, and
    // both editions were measured leaving the variable set afterwards, which
    // would hand the host's NEXT command this call's envelope.
    return (
      "$env:" + TRACE_BRIDGE_ENV + " = '" + bridge + "'; try { " + stripped +
      "\n} finally { Remove-Item Env:" + TRACE_BRIDGE_ENV + " -ErrorAction SilentlyContinue }"
    )
  }
  // The POSIX prefix binds to the first simple command and is a syntax
  // error before a compound one, so it is written only when that first
  // command is provably the search; anything else is left exactly as the
  // host wrote it, and that search runs on its local fallback identity.
  if (!SEARCH_LEADS_RE.test(stripped)) return null
  return TRACE_BRIDGE_ENV + "=" + bridge + " " + stripped
}
