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
  .replace(/\bAKIA[0-9A-Z]{16}\b/g, TRACE_REDACTED)
  .replace(/\b[rs]k_(?:live|test)_[A-Za-z0-9]{16,}/g, TRACE_REDACTED))

// A PEM private key block, markers included, as pkg/redact/pem.go removes
// it, before every other step: from a BEGIN marker whose label ends PRIVATE
// KEY (or PRIVATE KEY BLOCK) to the next private-key END marker, when no
// other BEGIN comes first; with none, to its line's end and on through the
// lines a body holds (base64, armor headers and the empty line after one,
// indentation, > markers and trailing blanks aside).
const TRACE_PEM_BEGIN = '-----BEGIN '
const TRACE_PEM_END = '-----END '
const tracePemMarkerEnd = (s, i) => {
  let j = i
  while (j < s.length && j - i < 64) {
    const c = s.charCodeAt(j)
    if (!((c >= 65 && c <= 90) || (c >= 48 && c <= 57) || c === 32)) break
    j++
  }
  if (!s.startsWith('-----', j)) return [-1, false]
  const label = s.slice(i, j)
  return [j + 5, label.endsWith('PRIVATE KEY') || label.endsWith('PRIVATE KEY BLOCK')]
}
const traceIsBase64Line = (line) => /^[A-Za-z0-9+\/=]*$/.test(line)
// A body line is read without leading blanks and > markers and without
// trailing blanks, so an indented or blockquoted key goes as a bare one does.
// (Loops, not /[\t ]+$/, which is quadratic in a run of blanks.)
const tracePemLineContent = (line) => {
  let i = 0
  while (i < line.length && (traceIsBlank(line.charCodeAt(i)) || line.charCodeAt(i) === 62)) i++
  return line.slice(i, traceTrimBlanksEnd(line, i, line.length))
}
const TRACE_PEM_HEADERS = ['Proc-Type:', 'DEK-Info:', 'Version:', 'Comment:', 'Hash:', 'Charset:', 'MessageID:']
const tracePemBodyEnd = (s, h) => {
  let end = traceLineEnd(s, h)
  let afterHeader = false
  while (end < s.length) {
    let start = end
    if (s.charCodeAt(start) === 13) start++
    if (start >= s.length || s.charCodeAt(start) !== 10) break
    start++
    const raw = s.slice(start, traceLineEnd(s, start))
    const line = tracePemLineContent(raw)
    if (line === '' && afterHeader) afterHeader = false
    else if (TRACE_PEM_HEADERS.some((p) => line.startsWith(p))) afterHeader = true
    else if (line !== '' && traceIsBase64Line(line)) afterHeader = false
    else return end
    end = start + raw.length
  }
  return end
}
const redactTracePrivateKeyBlocks = (text) => {
  let i = text.indexOf(TRACE_PEM_BEGIN)
  if (i < 0) return text
  let out = ''
  let last = 0
  let changed = false
  let nextEnd = -1
  let searched = false
  while (i >= 0) {
    let next = i + TRACE_PEM_BEGIN.length
    const [h, isPrivate] = tracePemMarkerEnd(text, next)
    if (h >= 0 && isPrivate) {
      if (!searched || (nextEnd >= 0 && nextEnd < h)) {
        nextEnd = text.indexOf(TRACE_PEM_END, h)
        searched = true
      }
      const nextBegin = text.indexOf(TRACE_PEM_BEGIN, h)
      let end = -1
      if (nextEnd >= 0 && (nextBegin < 0 || nextEnd < nextBegin)) {
        const [e, endPrivate] = tracePemMarkerEnd(text, nextEnd + TRACE_PEM_END.length)
        if (e >= 0 && endPrivate) end = e
      }
      if (end < 0) end = tracePemBodyEnd(text, h)
      out += text.slice(last, i) + TRACE_REDACTED
      last = next = end
      changed = true
    }
    i = text.indexOf(TRACE_PEM_BEGIN, next)
  }
  return changed ? out + text.slice(last) : text
}

// redactSecretAssignments: the value of NAME=value when a whole segment of
// NAME (split on _ - ., and a camelCase part by its last hump) is a secret word, or NAME is our own trace bridge,
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
// A camelCase part also counts by its last hump: the last capital after a
// lowercase letter or digit, or ending a run of capitals before a lowercase
// letter, to the end (accessToken, APIKey). The last word names what the
// value is; tokenCount and keyName hold a count and a name.
const traceIsUpper = (c) => c >= 65 && c <= 90
const traceIsLower = (c) => c >= 97 && c <= 122
const traceLastHump = (part) => {
  for (let i = part.length - 1; i > 0; i--) {
    const c = part.charCodeAt(i)
    const prev = part.charCodeAt(i - 1)
    if (!traceIsUpper(c)) continue
    if (traceIsLower(prev) || (prev >= 48 && prev <= 57) || (traceIsUpper(prev) && i + 1 < part.length && traceIsLower(part.charCodeAt(i + 1)))) return part.slice(i)
  }
  return part
}
const traceSecretName = (name) => {
  const upper = name.toUpperCase()
  if (TRACE_SECRET_NAMES.has(upper)) return true
  const parts = name.split(/[_.-]/).filter((part) => part !== '')
  const qualified = name === upper || parts.length >= 2
  return parts.some((part) => {
    const seg = part.toUpperCase()
    if (TRACE_SECRET_SEGMENTS.has(seg) || (qualified && TRACE_SECRET_SEGMENTS_QUALIFIED.has(seg))) return true
    const hump = traceLastHump(part)
    if (hump === part) return false
    const h = hump.toUpperCase()
    return TRACE_SECRET_SEGMENTS.has(h) || TRACE_SECRET_SEGMENTS_QUALIFIED.has(h)
  })
}
const traceIsWord = (c) => c === 95 || (c >= 48 && c <= 57) || (c >= 65 && c <= 90) || (c >= 97 && c <= 122)
const traceIsNameStart = (c) => c === 95 || (c >= 65 && c <= 90) || (c >= 97 && c <= 122)
const traceIsName = (c) => traceIsWord(c) || c === 45 || c === 46
const traceIsBlank = (c) => c === 32 || c === 9
const traceIsSpace = (c) => c === 32 || c === 9 || c === 10 || c === 12 || c === 13
// The quote at v closes at the returned index's left, or -1: not within
// maxLines line breaks. The escaping reading (a backslash escapes one unit,
// and in a double-quoted string so does a backtick) is taken when it closes
// on v's own line; otherwise the plain one, the next quote of the same kind,
// so 'C:\keys\' closes where a POSIX shell closes it.
const traceEscapedClosingQuote = (s, v) => {
  const q = s.charCodeAt(v)
  for (let i = v + 1; i < s.length; i++) {
    const c = s.charCodeAt(i)
    if (c === 10) return [-1, i]
    if (c === 92 || (c === 96 && q === 34)) {
      if (i + 1 < s.length && s.charCodeAt(i + 1) !== 10) i++
    } else if (c === q) {
      return [i + 1, 0]
    }
  }
  return [-1, s.length]
}
// One closer per value: where the escaping reading failed for a kind of
// quote, every quote of that kind on the rest of the line is escaped, and a
// part that opens at one fails the same way, so it is not read again.
const traceQuoteCloser = () => ({ failed: [false, false], from: [0, 0], stop: [0, 0] })
const traceClosingQuote = (s, v, maxLines, closer = traceQuoteCloser()) => {
  const k = s.charCodeAt(v) === 39 ? 1 : 0
  if (!closer.failed[k] || v <= closer.from[k] || v > closer.stop[k]) {
    const [escaped, stop] = traceEscapedClosingQuote(s, v)
    if (escaped >= 0) return escaped
    closer.failed[k] = true
    closer.from[k] = v
    closer.stop[k] = stop
  }
  const q = s.charCodeAt(v)
  let lines = 0
  for (let i = v + 1; i < s.length; i++) {
    const c = s.charCodeAt(i)
    if (c === q) return i + 1
    if (c === 10 && ++lines > maxLines) return -1
  }
  return -1
}
// Three quotes run to the next three of the same within the line cap.
const traceOpensThreeQuotes = (s, v) => v + 3 <= s.length && s.charCodeAt(v + 1) === s.charCodeAt(v) && s.charCodeAt(v + 2) === s.charCodeAt(v)
const traceTripleQuoteEnd = (s, v, maxLines) => {
  const q = s.charCodeAt(v)
  let lines = 0
  for (let i = v + 3; i + 2 < s.length; i++) {
    const c = s.charCodeAt(i)
    if (c === q) {
      if (s.charCodeAt(i + 1) === q && s.charCodeAt(i + 2) === q) return i + 3
    } else if (c === 10 && ++lines > maxLines) {
      return -1
    }
  }
  return -1
}
const traceLineEnd = (s, v) => {
  const i = s.indexOf('\n', v)
  if (i < 0) return s.length
  return i > v && s.charCodeAt(i - 1) === 13 ? i - 1 : i
}
// An unquoted value ends at whitespace, & or ;, except where a backslash
// keeps the unit after it (correct\ horse), a line break aside.
const traceIsEscape = (s, i) => s.charCodeAt(i) === 92 && i + 1 < s.length && s.charCodeAt(i + 1) !== 10 && s.charCodeAt(i + 1) !== 13
const traceRunEnd = (s, i) => {
  while (i < s.length) {
    if (traceIsEscape(s, i)) { i += 2; continue }
    const c = s.charCodeAt(i)
    if (traceIsSpace(c) || c === 38 || c === 59) break
    i++
  }
  return i
}
// Hand back a trailing , and any trailing ) ] } or quote the value has
// more of than it opened: they close something the value sits inside.
const traceTrimValueTail = (s, from, to) => {
  let paren = 0, bracket = 0, brace = 0, dquote = 0, squote = 0, backtick = 0
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
      case 96: backtick++; break
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
    else if (c === 96 && backtick % 2 === 1) backtick--
    else return to
    to--
  }
  return to
}
// A value that starts with a quote is read as a shell reads a word: quoted
// parts next to each other, a backslash-quote between them and characters
// joined to them are all one value. The first part may cross maxLines line
// breaks (three quotes run to the next three), -1 when it does not close
// within them; a later part must close on its own line, or its quote is just
// a character.
const traceQuotedWordEndWithin = (s, v, maxLines) => {
  const closer = traceQuoteCloser()
  let i = traceOpensThreeQuotes(s, v) ? traceTripleQuoteEnd(s, v, maxLines) : traceClosingQuote(s, v, maxLines, closer)
  if (i < 0) return -1
  let last = i
  while (i < s.length) {
    const c = s.charCodeAt(i)
    if (c === 34 || c === 39) {
      const closed = traceClosingQuote(s, i, 0, closer)
      if (closed >= 0) { i = last = closed; continue }
      i++
    } else if (traceIsEscape(s, i)) {
      i += 2
    } else if (traceIsSpace(c) || c === 38 || c === 59) {
      break
    } else {
      i++
    }
  }
  return traceTrimValueTail(s, last, i)
}
// A value inside the quote q that holds it ends at the quote that closes q
// on its line, or at a ; or & before that; blanks and what the tail rule
// hands back stay outside. from is past a quoted value's own quotes. The
// close is the first q no backslash (for " no backtick) stands right before,
// else the first q: only the character before is asked, so an answer holds
// for any start and can be remembered. -1 when q does not close on its line.
const traceEnclosedValueEnd = (s, from, q, words) => {
  let end = traceQuoteSearch(words.escaped, s, from, q)
  if (end < 0) {
    end = traceQuoteSearch(words.plain, s, from, q)
    if (end < 0) return -1
  }
  for (let i = from; i < end; i++) {
    const c = s.charCodeAt(i)
    if (c === 38 || c === 59) { end = i; break }
  }
  end = traceTrimBlanksEnd(s, from, end)
  return traceTrimValueTail(s, from, end)
}
const traceQuoteSearch = (f, s, v, q) => {
  if (f.used && f.q === q && v >= f.from && (f.at >= v || (f.at < 0 && v <= f.stop))) return f.at
  f.used = true
  f.q = q
  f.from = v
  f.at = -1
  let i = v
  for (; i < s.length && s.charCodeAt(i) !== 10; i++) {
    if (s.charCodeAt(i) === q && !(f.escaped && i > 0 && (s.charCodeAt(i - 1) === 92 || (q === 34 && s.charCodeAt(i - 1) === 96)))) {
      f.at = i
      break
    }
  }
  f.stop = i
  return f.at
}
// A quoted value's first part may cross the line cap only when its name
// starts its line; elsewhere it closes on its own line, else it is the rest
// of the line.
const traceSecretValueEnd = (s, v, words, startsLine) => {
  if (v >= s.length) return v
  let from = v
  const c = s.charCodeAt(v)
  if (c === 34 || c === 39) {
    from = traceQuotedWordEndWithin(s, v, startsLine ? TRACE_QUOTED_VALUE_MAX_LINES : 0)
    if (from < 0) from = traceLineEnd(s, v)
  } else if (c === 61 || traceIsSpace(c)) return v
  if (words.open) {
    const end = traceEnclosedValueEnd(s, from, words.head, words)
    if (end >= 0) return end
  }
  if (from > v) return from
  return traceTrimValueTail(s, v, traceRunEnd(s, v))
}
// The word that holds each name: what follows the last whitespace, a removed
// value counting as part of it. A word that opens with a quote, after any
// ( [ {, holds its names' values inside that quote while it stays open.
// Only the word is read, and not what a removed value held, so a second
// pass reads the same quote as open.
const traceWordScan = () => ({
  pos: 0, begun: false, head: 0, open: false,
  escaped: { escaped: true, used: false, q: 0, from: 0, at: -1, stop: 0 },
  plain: { escaped: false, used: false, q: 0, from: 0, at: -1, stop: 0 },
})
const traceWordAdvance = (w, s, to) => {
  for (; w.pos < to; w.pos++) {
    const c = s.charCodeAt(w.pos)
    if (traceIsSpace(c)) {
      w.begun = false
      w.head = 0
      w.open = false
    } else if (!w.begun && (c === 40 || c === 91 || c === 123)) {
      // still before the word's first character
    } else if (!w.begun) {
      w.begun = true
      if (c === 34 || c === 39) { w.head = c; w.open = true }
    } else if (c === w.head) {
      w.open = !w.open
    }
  }
}
// Past a removed value: its placeholder begins a word if none has begun and
// changes no quote.
const traceWordSkip = (w, to) => {
  if (to > w.pos) w.pos = to
  w.begun = true
}

// A name begins its line after indentation and nothing else on the line but
// a run of declaration words (export const, local -r, ENV, - , > ), and
// directly after $, $env: or ${env:. Only there is a quote after = certain
// to open a value and allowed to run across lines; elsewhere it may close a
// string ("PASSWORD=" + pw) and must not run on into the next line. The words
// are a fixed set because no later step changes them. Nothing before floor
// is read.
const traceIsLetter = (c) => traceIsUpper(c) || traceIsLower(c)
const TRACE_DECLARATION_KEYWORDS = new Set(['export', 'set', 'declare', 'typeset', 'local', 'readonly', 'env', 'arg', 'const', 'let', 'var'])
const traceDeclarationWord = (w) => {
  if (TRACE_DECLARATION_KEYWORDS.has(w.toLowerCase())) return true
  if (w === '-' || w === '*' || w === '+' || w === '>') return true
  if (w.length >= 2 && w[0] === '-') {
    for (let i = 1; i < w.length; i++) if (!traceIsLetter(w.charCodeAt(i))) return false
    return true
  }
  if (w.length >= 2 && (w[w.length - 1] === '.' || w[w.length - 1] === ')')) {
    for (let i = 0; i < w.length - 1; i++) if (w.charCodeAt(i) < 48 || w.charCodeAt(i) > 57) return false
    return true
  }
  return false
}
const traceAssignmentStartsLine = (s, floor, p) => {
  let i = p
  if (i - 6 >= floor && s.slice(i - 6, i).toLowerCase() === '${env:') i -= 6
  else if (i - 5 >= floor && s.slice(i - 5, i).toLowerCase() === '$env:') i -= 5
  else if (i - 1 >= floor && s.charCodeAt(i - 1) === 36) i -= 1
  for (;;) {
    let k = i
    while (k > floor && traceIsBlank(s.charCodeAt(k - 1))) k--
    if (k === 0 || (k > floor && s.charCodeAt(k - 1) === 10)) return true
    if (k === i) return false
    let j = k
    while (j > floor && !traceIsSpace(s.charCodeAt(j - 1))) j--
    if ((j > 0 && !traceIsSpace(s.charCodeAt(j - 1))) || !traceDeclarationWord(s.slice(j, k))) return false
    i = j
  }
}

// cmd's `set NAME=value` takes the rest of the line: only where cmd reads it
// as a command (set, after an optional @, at a line's start or after a
// backtick, & ( or |) and for a name with no lowercase letter, so prose
// that says "set password=x and ..." keeps its sentence. Nothing before
// floor is read: a removed value is a placeholder on the next pass.
const traceIsCmdSet = (s, floor, p, name) => {
  if (name !== name.toUpperCase() || p === 0 || !traceIsBlank(s.charCodeAt(p - 1))) return false
  let j = p
  while (j > 0 && traceIsBlank(s.charCodeAt(j - 1))) j--
  if (j - 3 < floor || s.slice(j - 3, j).toLowerCase() !== 'set') return false
  let k = j - 3
  if (k > floor && s.charCodeAt(k - 1) === 64) k--
  while (k > floor && traceIsBlank(s.charCodeAt(k - 1))) k--
  return k === 0 || (k > floor && '\n`&(|'.includes(s[k - 1]))
}
// It ends at the line's end, &, | or a backtick; blanks and what the tail
// rule hands back stay outside.
const traceCmdValueEnd = (s, v) => {
  let end = v
  while (end < s.length && !'\n\r&|`'.includes(s[end])) end++
  end = traceTrimBlanksEnd(s, v, end)
  return traceTrimValueTail(s, v, end)
}
// The secret value behind the = at e, as [start, end], or null. The name is
// read backwards from e and never past floor, the end of the last value
// removed.
const traceSecretValueAt = (s, floor, e, words) => {
  let k = e
  while (k > floor && traceIsBlank(s.charCodeAt(k - 1))) k--
  let p = -1
  let name = ''
  let psEnv = false
  if (k > floor && s.charCodeAt(k - 1) === 125) {
    // PowerShell's braced form, ${env:NAME}, spaces around = allowed.
    let r = k - 1
    while (r > floor && traceIsName(s.charCodeAt(r - 1))) r--
    if (r === k - 1 || !traceIsNameStart(s.charCodeAt(r)) || !traceIsWord(s.charCodeAt(k - 2)) || r < 6 || !/^\$\{[Ee][Nn][Vv]:$/.test(s.slice(r - 6, r))) return null
    p = r
    name = s.slice(r, k - 1)
    psEnv = true
  } else {
    let r = k
    while (r > floor && traceIsName(s.charCodeAt(r - 1))) r--
    if (r === k || !traceIsWord(s.charCodeAt(k - 1))) return null
    for (let q = r; q < k; q++) {
      if (traceIsNameStart(s.charCodeAt(q)) && (q === 0 || !traceIsWord(s.charCodeAt(q - 1)))) { p = q; break }
    }
    if (p < 0) return null
    name = s.slice(p, k)
    psEnv = p >= 5 && /^\$[Ee][Nn][Vv]:$/.test(s.slice(p - 5, p))
  }
  // The bridge's name alone or as the last part of a dotted or hyphenated
  // name: a later step can remove what stands before it (an email's domain),
  // and a second pass must not then find what the first did not.
  const upperName = name.toUpperCase()
  const bridge = upperName === TRACE_BRIDGE_ENV || upperName.endsWith('.' + TRACE_BRIDGE_ENV) || upperName.endsWith('-' + TRACE_BRIDGE_ENV)
  let v = e + 1
  while (v < s.length && traceIsBlank(s.charCodeAt(v))) v++
  // Blanks around = belong to PowerShell, the bridge, and a quoted value
  // (TOML's and Python's password = "x"); not to code or prose.
  if (!psEnv && !bridge && s.charCodeAt(v) !== 34 && s.charCodeAt(v) !== 39) {
    if (k < e) return null
    v = e + 1
  }
  // An Azure shared access signature's sig, only as a URL query parameter.
  const sas = p > 0 && (s.charCodeAt(p - 1) === 63 || s.charCodeAt(p - 1) === 38) && name.toLowerCase() === 'sig'
  if (!bridge && !traceSecretName(name) && !sas) return null
  traceWordAdvance(words, s, p)
  const c = s.charCodeAt(v)
  const end = v < s.length && !traceIsSpace(c) && c !== 61 && traceIsCmdSet(s, floor, p, name)
    ? traceCmdValueEnd(s, v)
    : traceSecretValueEnd(s, v, words, traceAssignmentStartsLine(s, floor, p))
  if (end === v || s.slice(v, end) === TRACE_REDACTED) return null
  return [v, end]
}
const redactTraceSecretAssignments = (text) => {
  let e = text.indexOf('=')
  if (e < 0) return text
  let out = ''
  let last = 0
  let changed = false
  const words = traceWordScan()
  while (e >= 0) {
    let next = e + 1
    const found = traceSecretValueAt(text, last, e, words)
    if (found) {
      out += text.slice(last, found[0]) + TRACE_REDACTED
      last = next = found[1]
      changed = true
      traceWordAdvance(words, text, found[0])
      traceWordSkip(words, found[1])
    }
    e = text.indexOf('=', next)
  }
  return changed ? out + text.slice(last) : text
}

// redactSecretKeys: the value of a secret key written with a colon, as
// pkg/redact/keys.go has it. A key at a line's start (after indentation and
// an optional - marker), quoted or not, then a colon and a blank or a quote:
// the rest of the line.
// A key where an object's or a call's member opens, then a colon and a quoted
// value closing on its own line: that quoted string, with parts that follow
// it directly in the same quote. Never a reference or a placeholder.
const traceStartsLine = (s, floor, i) => {
  while (i > floor && traceIsBlank(s.charCodeAt(i - 1))) i--
  if (i > floor && s.charCodeAt(i - 1) === 45) {
    i--
    while (i > floor && traceIsBlank(s.charCodeAt(i - 1))) i--
  }
  return i === 0 || (i > floor && s.charCodeAt(i - 1) === 10)
}
// A mid-line key's quoted value closes on its own line or is no value.
const traceQuotedStringEnd = (s, v) => {
  const closer = traceQuoteCloser()
  let i = traceClosingQuote(s, v, 0, closer)
  if (i < 0) return -1
  while (i < s.length && s.charCodeAt(i) === s.charCodeAt(v)) {
    const j = traceClosingQuote(s, i, 0, closer)
    if (j < 0) break
    i = j
  }
  return i
}
// A mid-line key counts only where a member opens: after { , ( or [, or where
// a statement or a code span does, after ; or a backtick. Anywhere else it is
// prose or the inside of a string ("Password: " in input(...)), whose closing
// quote must not be read as the value's opening one.
const traceOpensMember = (s, floor, i) => {
  while (i > floor && traceIsBlank(s.charCodeAt(i - 1))) i--
  return i > floor && '{,([;`'.includes(s[i - 1])
}
// A reference or a placeholder is not the secret: a GitHub Actions or
// template expression in double braces, ${TOKEN}, $TOKEN, <pad>, a type name,
// a size (5 bytes), or a block that only opens ({, [, |, >-). A reference
// that carries a literal is: a literal default, assignment or alternate in
// an expansion (the :- - := = :+ + forms, and Spring's plain : unless its
// word starts with a digit, a blank or ?, which is bash's substring; :?
// stays, its word is a message),
// and an expression in double braces holding a quoted string. A variable is
// $ and capitals, digits and _, or letters and _ with no digit: $ecret123
// and $Pa55word are values. $ and the placeholder is a variable whose name a
// later step replaced, so a second pass keeps what the first kept.
const TRACE_LITERAL_DEFAULT = /^\$\{[A-Za-z_][A-Za-z0-9_.]*(:?[-=+]|:)([^}\n]*)\}$/
const TRACE_BARE_VARIABLE = /^(?:\$(?:[A-Z_][A-Z0-9_]*|[A-Za-z_]+|\[REDACTED\]))?$/
const TRACE_SECRET_REFERENCE = /^(?:\$?\{\{[^{}"'`\n]*\}\}|\$\{[^}\n]*\}|\$(?:[A-Z_][A-Z0-9_]*|[A-Za-z_]+|\[REDACTED\])|<[^<>\t\n ]+>|string|str|number|int|integer|bool|boolean|any|unknown|bytes|float|double|char|String|[A-Za-z_][A-Za-z0-9_:]*<[^\n]*>|[0-9]+ bytes|[{\[(|>+-]+)$/
const traceNotASecret = (v) => {
  if (v.endsWith(',') || v.endsWith(';')) v = v.slice(0, -1)
  if (v.length >= 2 && (v[0] === '"' || v[0] === "'") && v[v.length - 1] === v[0]) v = v.slice(1, -1)
  const d = TRACE_LITERAL_DEFAULT.exec(v)
  if (d && !TRACE_BARE_VARIABLE.test(d[2]) && (d[1] !== ':' || !/^[0-9\t ?]/.test(d[2]))) return false
  return TRACE_SECRET_REFERENCE.test(v)
}
const traceSecretKeyValueAt = (s, floor, c) => {
  let k = c
  while (k > floor && traceIsBlank(s.charCodeAt(k - 1))) k--
  let name
  let keyStart
  const kc = s.charCodeAt(k - 1)
  if (k > floor && (kc === 34 || kc === 39)) {
    let r = k - 1
    while (r > floor && traceIsName(s.charCodeAt(r - 1))) r--
    if (r === k - 1 || r === floor || s.charCodeAt(r - 1) !== kc || !traceIsNameStart(s.charCodeAt(r)) || !traceIsWord(s.charCodeAt(k - 2))) return null
    name = s.slice(r, k - 1)
    keyStart = r - 1
  } else {
    if (k < c) return null
    let r = k
    while (r > floor && traceIsName(s.charCodeAt(r - 1))) r--
    if (r === k || !traceIsWord(s.charCodeAt(k - 1))) return null
    let p = -1
    for (let q = r; q < k; q++) {
      if (traceIsNameStart(s.charCodeAt(q)) && (q === 0 || !traceIsWord(s.charCodeAt(q - 1)))) { p = q; break }
    }
    if (p < 0) return null
    name = s.slice(p, k)
    keyStart = p
  }
  if (!traceSecretName(name)) return null
  let v = c + 1
  while (v < s.length && traceIsBlank(s.charCodeAt(v))) v++
  if (v >= s.length || s.charCodeAt(v) === 10 || s.charCodeAt(v) === 13) return null
  const quoted = s.charCodeAt(v) === 34 || s.charCodeAt(v) === 39
  let end
  if (traceStartsLine(s, floor, keyStart) && (v > c + 1 || quoted)) {
    end = traceTrimValueTail(s, v, traceTrimBlanksEnd(s, v, traceLineEnd(s, v)))
  } else if (quoted && traceOpensMember(s, floor, keyStart)) {
    end = traceQuotedStringEnd(s, v)
    if (end < 0) return null
  } else {
    return null
  }
  if (end === v || s.slice(v, end) === TRACE_REDACTED || traceNotASecret(s.slice(v, end))) return null
  return [v, end]
}
// redactSecretFlags: a long flag named as a secret's, then its value as the
// next word on the line (curl --api-key VALUE). Not a value: one starting
// with - or =, or one of lowercase letters only ("the --password flag").
// What stands before -- is not asked, as a later step can remove it. The
// flag rule runs first, then the key rule, then the assignment rule, so no
// removal changes what an earlier rule read.
// A flag's name counts by its last segment (or that segment's last camelCase
// word): --api-key takes a secret, --key-name, --secret-id, --token-ttl and
// --passphrase-file do not. A --with- or --no- flag takes no value. A last
// word naming the value's form (string, value, phrase) asks the word before
// it: --secret-string, --pass-phrase.
const TRACE_VALUE_FORM_WORDS = new Set(['STRING', 'VALUE', 'PHRASE'])
const traceSecretFlagName = (name) => {
  const lower = name.toLowerCase()
  if (lower.startsWith('with-') || lower.startsWith('no-')) return false
  const upper = name.toUpperCase()
  if (TRACE_SECRET_NAMES.has(upper)) return true
  const parts = name.split(/[_.-]/).filter((part) => part !== '')
  // A name made only of separators, as in `--_ x`, has no word to be a
  // secret's, and reading its last word would throw.
  if (parts.length === 0) return false
  const last = parts[parts.length - 1]
  const hump = traceLastHump(last)
  const qualified = name === upper || parts.length >= 2 || hump !== last
  if ([last.toUpperCase(), hump.toUpperCase()].some((seg) => TRACE_SECRET_SEGMENTS.has(seg) || (qualified && TRACE_SECRET_SEGMENTS_QUALIFIED.has(seg)))) return true
  const before = hump !== last ? last.slice(0, last.length - hump.length) : parts.length >= 2 ? parts[parts.length - 2] : ''
  if (before === '' || !TRACE_VALUE_FORM_WORDS.has(hump.toUpperCase())) return false
  const seg = traceLastHump(before).toUpperCase()
  return TRACE_SECRET_SEGMENTS.has(seg) || TRACE_SECRET_SEGMENTS_QUALIFIED.has(seg)
}
const traceSecretFlagValueAt = (s, i) => {
  const n = i + 2
  if (n >= s.length || !traceIsNameStart(s.charCodeAt(n))) return null
  let j = n
  while (j < s.length && traceIsName(s.charCodeAt(j))) j++
  const none = { nameEnd: j }
  if (!traceIsWord(s.charCodeAt(j - 1)) || j >= s.length || !traceIsBlank(s.charCodeAt(j))) return none
  const name = s.slice(n, j)
  if (!traceSecretFlagName(name)) return none
  let v = j
  while (v < s.length && traceIsBlank(s.charCodeAt(v))) v++
  const c = s.charCodeAt(v)
  if (v >= s.length || traceIsSpace(c) || '-=<>|'.includes(s[v]) || s.startsWith('$(', v)) return none
  let end
  if (c === 34 || c === 39) {
    // A quoted value closes on its own line, or the quote closes a string
    // ("mysql --password " + pw) and must not run on into the next line.
    end = traceQuotedWordEndWithin(s, v, 0)
    if (end < 0) return none
  } else {
    end = traceTrimValueTail(s, v, traceRunEnd(s, v))
    const word = s.slice(v, end)
    if (/^[a-z]*$/.test(word)) return none
    // A help text's metavar: the flag's own name in capitals (--token TOKEN).
    if (word === word.toUpperCase() && word.replaceAll('-', '_') === name.toUpperCase().replaceAll('-', '_')) return none
  }
  if (end === v || s.slice(v, end) === TRACE_REDACTED) return none
  return { start: v, end, nameEnd: j }
}
// The key rule's scanner: every colon, read once, left to right, never
// reaching back past the last value removed.
const traceRedactAt = (text, marker, valueAt) => {
  let at = text.indexOf(marker)
  if (at < 0) return text
  let out = ''
  let last = 0
  let changed = false
  while (at >= 0) {
    let next = at + marker.length
    const found = valueAt(text, last, at)
    if (found) {
      out += text.slice(last, found[0]) + TRACE_REDACTED
      last = next = found[1]
      changed = true
    }
    at = text.indexOf(marker, next)
  }
  return changed ? out + text.slice(last) : text
}
const redactTraceSecretKeys = (text) => traceRedactAt(text, ':', traceSecretKeyValueAt)
// A flag's name is read whole, across -, so a -- inside it starts no flag
// and a scan that finds none resumes past the name: linear on --a--a--a.
const redactTraceSecretFlags = (text) => {
  let at = text.indexOf('--')
  if (at < 0) return text
  let out = ''
  let last = 0
  let changed = false
  while (at >= 0) {
    let next = at + 2
    const found = traceSecretFlagValueAt(text, at)
    if (found && found.start !== undefined) {
      out += text.slice(last, found.start) + TRACE_REDACTED
      last = next = found.end
      changed = true
    } else if (found && found.nameEnd > next) {
      next = found.nameEnd
    }
    at = text.indexOf('--', next)
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

// redactEnvTables: PowerShell's Get-ChildItem Env: table (and any hashtable
// it prints): under a Name/Value header and a rule of dashes, each row's
// value is the rest of its line after the name and two blanks or a tab. A
// name with no value (an empty variable, a $null entry) and a line whose
// blanks reach exactly the header's Value column (-Wrap's continuation,
// whose text goes too) continue the table; the first other line ends it
// (an empty line, a fence, prose, a numbered item).
const TRACE_ENV_TABLE_HEADER = /^[\t ]*Name[\t ]+Value[\t ]*$/
const TRACE_ENV_TABLE_RULE = /^[\t ]*-+[\t ]+-+[\t ]*$/
const TRACE_ENV_TABLE_ROW = /^([\t ]*[^\t\n\f\r ]+(?:\t|  )[\t ]*)([^\t\n\f\r ][^\n]*)$/
const TRACE_ENV_TABLE_NAME_ONLY = /^[\t ]*[A-Za-z_](?:[A-Za-z0-9_.()-]*[A-Za-z0-9_)])?[\t ]*$/
const redactTraceEnvTables = (text) => {
  if (!text.includes('Value')) return text
  const lines = text.split('\n')
  const bare = (i) => lines[i].endsWith('\r') ? lines[i].slice(0, -1) : lines[i]
  let changed = false
  for (let i = 0; i + 1 < lines.length; i++) {
    const header = bare(i)
    if (!TRACE_ENV_TABLE_HEADER.test(header) || !TRACE_ENV_TABLE_RULE.test(bare(i + 1))) continue
    const column = header.indexOf('Value')
    let k = i + 2
    for (; k < lines.length; k++) {
      const line = bare(k)
      const cr = line === lines[k] ? '' : '\r'
      let indent = 0
      while (indent < line.length && traceIsBlank(line.charCodeAt(indent))) indent++
      if (indent === column && indent < line.length) {
        if (line.slice(indent) !== TRACE_REDACTED) { lines[k] = line.slice(0, indent) + TRACE_REDACTED + cr; changed = true }
        continue
      }
      if (TRACE_ENV_TABLE_NAME_ONLY.test(line)) continue
      const m = TRACE_ENV_TABLE_ROW.exec(line)
      if (!m) break
      if (m[2] !== TRACE_REDACTED) { lines[k] = m[1] + TRACE_REDACTED + cr; changed = true }
    }
    i = k - 1
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

// A Windows separator may be doubled, as Python's repr and JSON print a
// path, and a name with spaces is taken whole when the path goes on past it:
// up to three more words with no quote, colon, slash or backtick, then a
// backslash. Otherwise only the first word goes.
const redactTraceHomePaths = (text) => text
  .replace(/([A-Za-z\u212A\u017F]:\\+[Uu][Ss\u017F][Ee][Rr][Ss\u017F]\\+)[^\\\t\n\f\r ]+(?:(?: [^\\\t\n\f\r :"'`\/]+){1,3}(\\))?/g, '$1' + TRACE_REDACTED + '$2')
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
    out.accountHome = new RegExp('(\\/Users\\/|\\/home\\/|[A-Za-z\\u212A\\u017F]:\\\\+[Uu][Ss\\u017F][Ee][Rr][Ss\\u017F]\\\\+)' +
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
  redactTraceLocalIdentity(redactTraceHomePaths(redactTraceAccountHomes(redactTraceEmails(redactTraceEnvTables(redactTraceEnvDumps(redactTraceSecretAssignments(redactTraceSecretKeys(redactTraceSecretFlags(scrubTraceCommon(redactTracePrivateKeyBlocks(text)))))))), identity)), identity)

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

// The only value a recognized assignment may carry: the base64url alphabet
// the bridge is encoded in, with no quote, escape or shell metacharacter. A
// quote-unaware cut through any other value can end inside a string the
// shell would have read as data, and turn that data into code. One rule with
// bridgeValue in bridge.go, pinned by TestBridgeGuardsAgree.
const BRIDGE_VALUE = "[A-Za-z0-9_-]*"

// A standalone bridge assignment at the START of the command, in each
// declared shell's syntax, with the rest of the command after it.
const BRIDGE_ASSIGNMENT_RE = new RegExp(
  "^(?:" +
    // POSIX: NAME=value as a leading word.
    TRACE_BRIDGE_ENV + "=" + BRIDGE_VALUE + "[ \\t\\n]+" +
    "|" +
    // PowerShell: $env:NAME = '…' or "…" or a bare word, as its own
    // statement, ended by a newline or a semicolon.
    "\\$env:" + TRACE_BRIDGE_ENV + "\\s*=\\s*(?:'" + BRIDGE_VALUE + "'|\"" + BRIDGE_VALUE + "\"|" + BRIDGE_VALUE + ")\\s*[;\\n]\\s*" +
    "|" +
    // cmd: set NAME=value as its own command.
    "set\\s+" + TRACE_BRIDGE_ENV + "=" + BRIDGE_VALUE + "[ \\t]*(?:&+|\\n)\\s*" +
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

// The variable's name anywhere, in any ASCII case: PowerShell and cmd read
// an environment variable's name without regard to case, and PowerShell
// reaches it through spellings no assignment shape keeps up with (blanks,
// a backtick continuation or a comment before the =, ${env:...},
// Set-Item). One rule with bridgeMentionRe in bridge.go, pinned by
// TestBridgeGuardsAgree.
const BRIDGE_MENTION_RE = new RegExp(TRACE_BRIDGE_ENV, "i")

// carriesUnremovableBridge: the command still mentions the variable in a
// position this cannot prove is a standalone assignment. The adapter leaves
// such a command exactly as it found it.
const carriesUnremovableBridge = (cmd) => BRIDGE_MENTION_RE.test(cmd)

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
