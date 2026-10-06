package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

// Synthetic values assembled at runtime so no credential-shaped literal
// exists in the source (gosec G101, and the spirit of testdata/README.md).
var (
	fakeKey    = "sk-or-v1-" + strings.Repeat("0", 4) + "synthetic"
	fakeBearer = strings.Join([]string{"Bearer", "0000synthetic0000"}, " ")
)

func logAndCapture(t *testing.T, logFn func(l *slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	logFn(NewLogger(&buf, slog.LevelDebug))
	return buf.String()
}

func mustNotContain(t *testing.T, out string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("log output contains %q:\n%s", s, out)
		}
	}
}

func TestDenylistedKeys(t *testing.T) {
	for _, key := range []string{
		"authorization", "Authorization", "AUTHORIZATION",
		"api_key", "token", "cookie", "set-cookie", "proxy-authorization",
	} {
		out := logAndCapture(t, func(l *slog.Logger) {
			l.Info("m", key, "secret-value-123")
		})
		mustNotContain(t, out, "secret-value-123")
		if !strings.Contains(out, "[REDACTED]") {
			t.Errorf("key %q: expected placeholder in output:\n%s", key, out)
		}
	}
}

func TestCredentialPatternsInValues(t *testing.T) {
	out := logAndCapture(t, func(l *slog.Logger) {
		l.Info("upstream said "+fakeKey,
			"detail", "request with "+fakeKey+" failed",
			"hdr", fakeBearer,
		)
	})
	mustNotContain(t, out, fakeKey, "0000synthetic0000")
}

// Batch-1 T3: the shapes redact.String missed before the trace path wired
// it in — sr- (this system's own router key), a dashless sk- key (no
// second word segment between the prefix and the random run), a GitHub
// token, an AWS access key id, a bare JWT, an email address, and a home
// directory path. Each assembled at runtime so no credential-shaped
// literal exists in source (gosec G101).
func TestNewCredentialShapesAreScrubbed(t *testing.T) {
	srKey := "sr-" + strings.Repeat("c", 24) + "canary"
	dashlessSK := "sk" + "-" + strings.Repeat("1", 32)
	ghToken := "ghp_" + strings.Repeat("a", 36)
	awsKey := "AKIA" + strings.Repeat("Q", 16)
	jwt := "eyJ" + strings.Repeat("h", 10) + "." + strings.Repeat("p", 10) + "." + strings.Repeat("s", 10)
	email := "someone" + "@" + "example.com"
	homePath := "/Users/" + "realname" + "/.aws/credentials"

	for name, secret := range map[string]string{
		"sr- key": srKey, "dashless sk- key": dashlessSK, "github token": ghToken,
		"aws key": awsKey, "jwt": jwt, "email": email,
	} {
		if out := String("value: " + secret + " end"); strings.Contains(out, secret) {
			t.Errorf("%s not scrubbed: %q", name, out)
		}
	}

	out := String("file: " + homePath)
	if strings.Contains(out, homePath) {
		t.Errorf("home path not scrubbed: %q", out)
	}
	if !strings.Contains(out, ".aws/credentials") {
		t.Errorf("home path scrubbing ate more than the username: %q", out)
	}
}

func TestURLUserinfoScrubbed(t *testing.T) {
	out := logAndCapture(t, func(l *slog.Logger) {
		l.Info("dial", "url", "https://user:pass@openrouter.ai/api")
	})
	mustNotContain(t, out, "user:pass")
	if !strings.Contains(out, "openrouter.ai") {
		t.Errorf("host should survive redaction:\n%s", out)
	}
}

func TestHeaderAndUserinfoValuesRefused(t *testing.T) {
	h := http.Header{"Authorization": []string{fakeBearer}, "X-Ok": []string{"fine"}}
	out := logAndCapture(t, func(l *slog.Logger) {
		l.Info("m", "headers", h, "user", url.UserPassword("u", "p"))
	})
	// The entire header map is refused, including innocuous values.
	mustNotContain(t, out, "0000synthetic0000", "fine", `"p"`)
}

func TestGroupsAreWalked(t *testing.T) {
	out := logAndCapture(t, func(l *slog.Logger) {
		l.Info("m", slog.Group("req", slog.String("authorization", "sec"), slog.String("note", fakeKey)))
	})
	mustNotContain(t, out, `"sec"`, fakeKey)
}

func TestWithAttrsScrubbed(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(&buf, slog.LevelDebug).With("api_key", fakeKey)
	l.Info("m")
	mustNotContain(t, buf.String(), fakeKey)
}

func TestErrorRedaction(t *testing.T) {
	err := errors.New("Get \"https://user:pass@host/x\": auth " + fakeKey)
	red := Error(err)
	if strings.Contains(red.Error(), fakeKey) || strings.Contains(red.Error(), "user:pass") {
		t.Errorf("Error() leaked: %s", red)
	}
	if Error(nil) != nil {
		t.Error("Error(nil) must be nil")
	}
	// The original text must not be reachable through the chain.
	if errors.Unwrap(red) != nil {
		t.Error("redacted error must not unwrap to the original")
	}
}

func TestStdLoggerAdapter(t *testing.T) {
	var buf bytes.Buffer
	std := NewStdLogger(NewLogger(&buf, slog.LevelDebug), slog.LevelError)
	std.Printf("http: proxy error: dial https://u:p@x.test with %s", fakeKey)
	mustNotContain(t, buf.String(), fakeKey, "u:p")
	if !strings.Contains(buf.String(), "proxy error") {
		t.Errorf("message body lost:\n%s", buf.String())
	}
}

func TestErrorAttrScrubbed(t *testing.T) {
	out := logAndCapture(t, func(l *slog.Logger) {
		l.Error("fail", "err", errors.New("boom "+fakeKey))
	})
	mustNotContain(t, out, fakeKey)
	if !strings.Contains(out, "boom") {
		t.Errorf("error text lost:\n%s", out)
	}
}

// PR dropin-miner#1 review: six false positives measured against realistic assistant
// text. Each of these must now survive String() (and, except the bearer
// case, TraceText()) byte-for-byte — this is corpus damage, not a secret.
func TestFalsePositivesMeasuredInReviewSurvive(t *testing.T) {
	cases := map[string]string{
		"git ssh remote":       "git clone git@github.com:acme/repo.git",
		"ssh command":          "ssh deploy@prod.example.com",
		"css accessibility":    `class="sr-only-focusable"`,
		"bcp-47 locale tag":    "locale sr-Latn-RS is Serbian (Latin, Serbia)",
		"generic cache key":    "invalidating cache key sk-cache-entry-42",
		"url path, not a home": "see https://example.com/home/page for docs",
	}
	for name, text := range cases {
		if got := String(text); got != text {
			t.Errorf("%s: String() altered non-secret text:\n  in:  %q\n  out: %q", name, text, got)
		}
		if got := TraceText(text); got != text {
			t.Errorf("%s: TraceText() altered non-secret text:\n  in:  %q\n  out: %q", name, text, got)
		}
	}
}

// The one case that's supposed to differ between the two: ordinary prose
// about tokens is common trajectory data and must survive TraceText, but
// String (the log path, where "bearer" is far more likely to precede an
// actual header value) keeps redacting it.
func TestBearerProseSurvivesTraceTextButNotString(t *testing.T) {
	text := "bearer tokens expire"
	if got := TraceText(text); got != text {
		t.Errorf("TraceText altered ordinary prose about bearer tokens: %q", got)
	}
	if got := String(text); got == text || !strings.Contains(got, placeholder) {
		t.Errorf("String no longer redacts a bearer-shaped value: %q", got)
	}
}

// A real credential riding right next to a false-positive shape must still
// be caught — the narrowing must not have gone too far the other way.
func TestRealSecretsStillCaughtAlongsideFalsePositiveShapes(t *testing.T) {
	secret := "sk-or-v1-" + strings.Repeat("a", 24) + "SECRET"
	text := "class sr-only-focusable, and the key is " + secret
	got := TraceText(text)
	if strings.Contains(got, secret) {
		t.Errorf("real secret survived alongside a false-positive shape: %q", got)
	}
	if !strings.Contains(got, "sr-only-focusable") {
		t.Errorf("the false-positive shape was collateral damage: %q", got)
	}
}

// Windows home paths (C:\Users\<name>\...) — the Unix-only pattern missed
// these entirely on a repo that ships Windows binaries.
func TestWindowsHomePathScrubbed(t *testing.T) {
	path := `C:\Users\` + "realname" + `\.aws\credentials`
	got := TraceText(path)
	if strings.Contains(got, "realname") {
		t.Errorf("Windows username not scrubbed: %q", got)
	}
	if !strings.Contains(got, `C:\Users\`) || !strings.Contains(got, ".aws") {
		t.Errorf("Windows path scrubbing ate more than the username: %q", got)
	}
}

// The email/home-path heuristics must not swallow a genuinely planted
// secret sitting right next to them — the exclusions are about SHAPE
// (immediately followed by ':', preceded by a remote-access verb, part of
// a URL path), not about disabling redaction near those shapes generally.
func TestFalsePositiveExclusionsDoNotShadowRealSecretsNearby(t *testing.T) {
	secret := "sk-or-v1-" + strings.Repeat("b", 24) + "SECRET"
	text := "ssh deploy@prod.example.com and also the key " + secret
	got := TraceText(text)
	if strings.Contains(got, secret) {
		t.Errorf("real secret near an excluded shape was not redacted: %q", got)
	}
}

// TestMain takes the machine's own names out of every test in the package.
// TraceText searches for the hostname and the account it runs under, so a
// guarantee about ordinary text would otherwise depend on the machine: on a
// host called prod, or under an account called value, a test whose text
// says either would fail. A test that needs a name sets it with
// SetLocalIdentity and restores this.
func TestMain(m *testing.M) {
	restore := SetLocalIdentity("", "")
	code := m.Run()
	restore()
	os.Exit(code)
}

// traceCase is one row of testdata/trace_cases.json: the identity the
// scrubber is told, the text, and exactly what must come out. The same rows
// run through the JavaScript scrubber in cmd/jevlin
// (TestSharedTraceSourceAgreesOnEveryTraceCase), so the two languages are
// held to one table rather than to two that can drift.
type traceCase struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Account string `json:"account"`
	In      string `json:"in"`
	Want    string `json:"want"`
}

func loadTraceCases(t *testing.T) []traceCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/trace_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []traceCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no trace cases")
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.Name] {
			t.Fatalf("two trace cases are named %q", c.Name)
		}
		seen[c.Name] = true
	}
	return cases
}

// Every row of the shared table, on the Go side. A row whose want equals
// its input is a survivor: text the scrubber must leave exactly as it is.
func TestTraceCases(t *testing.T) {
	for _, c := range loadTraceCases(t) {
		restore := SetLocalIdentity(c.Host, c.Account)
		if got := TraceText(c.In); got != c.Want {
			t.Errorf("%s: TraceText(%q) with identity %q/%q\n  got:  %q\n  want: %q", c.Name, c.In, c.Host, c.Account, got, c.Want)
		}
		restore()
	}
}

// A non-ASCII name is not searched for: Go and JavaScript fold case
// differently outside ASCII, so a rule for it could not be held to the
// same bytes in both. The text carries both names, so the test can fail.
func TestNonASCIILocalNamesAreNotSearchedFor(t *testing.T) {
	defer SetLocalIdentity("héllo-box", "josé")()
	text := "on héllo-box as josé: ssh josé@db1, USER=josé, héllo-box.example"
	if got := TraceText(text); got != text {
		t.Errorf("a non-ASCII name was removed: %q -> %q", text, got)
	}
}

// The log path is this client's own words and keeps its own rules: none of
// the trajectory-only steps run there.
func TestTheLogPathIsUnchangedByTheTraceRules(t *testing.T) {
	defer SetLocalIdentity("Build-Box7", "mwhitlock")()
	text := "DATABASE_PASSWORD=hunter2 as mwhitlock on Build-Box7"
	if got := String(text); got != text {
		t.Errorf("String applied a trace-only rule: %q", got)
	}
}

// TraceText removes what it removes once: its own output, scrubbed again,
// comes back unchanged. A rule whose match could swallow the start of the
// next one, or whose placeholder could read as a value of its own, fails
// this on the second pass. Every row of the shared table is an input, with
// its identity, and so are the rows run together, so that a value removed
// on one row meets the text of the next.
func TestTraceTextIsIdempotent(t *testing.T) {
	cases := loadTraceCases(t)
	check := func(name, host, account, text string) {
		t.Helper()
		restore := SetLocalIdentity(host, account)
		defer restore()
		once := TraceText(text)
		if twice := TraceText(once); twice != once {
			t.Errorf("%s: a second pass changed the text\n  once:  %q\n  twice: %q", name, once, twice)
		}
	}
	var plain []string
	for _, c := range cases {
		check(c.Name, c.Host, c.Account, c.In)
		if c.Host == "" && c.Account == "" {
			plain = append(plain, c.In)
		}
	}
	for _, sep := range []string{"\n", "", ",", "&", " "} {
		check(fmt.Sprintf("the rows joined by %q", sep), "", "", strings.Join(plain, sep))
	}
}

// A line that opens a quote it does not close on its own reading of the
// rules (a prompt string, an error message, a label ending in a key and a
// colon) must not reach into the next line: a review found
// pw = input("Password: ") followed by DB_PASSWORD="..." keeping the
// password, the key rule having taken the closing quote of "Password: " for
// an opening one and run to the quote on the next line. Every row of the
// shared table that removes something is put on the line after each prompt
// line of testdata/trace_prompt_lines.json, and what comes out after the
// prompt line must be exactly the row's own answer, and the prompt line
// exactly its own: a flag's value read from the prompt string's closing
// quote changed both lines. The lines are strings and calls that end in a
// key and a colon, a flag, or a name and `=`, so every rule that reads a
// quoted value meets a quote it did not open. cmd/jevlin's
// TestTheSharedSourceLetsNoPromptLineReachTheNextLine holds JavaScript to
// the same.
func TestNoPromptLineReachesTheNextLine(t *testing.T) {
	raw, err := os.ReadFile("testdata/trace_prompt_lines.json")
	if err != nil {
		t.Fatal(err)
	}
	var prompts []string
	if err := json.Unmarshal(raw, &prompts); err != nil {
		t.Fatal(err)
	}
	for _, c := range loadTraceCases(t) {
		if c.Want == c.In {
			continue
		}
		restore := SetLocalIdentity(c.Host, c.Account)
		for _, p := range prompts {
			got := TraceText(p + "\n" + c.In)
			before, after, _ := strings.Cut(got, "\n")
			if strings.Count(p, "\n") == 0 && after != c.Want {
				t.Errorf("%s after %q:\n  got:  %q\n  want: %q", c.Name, p, after, c.Want)
			}
			if own := TraceText(p); before != own {
				t.Errorf("%q before %s:\n  got:  %q\n  want: %q", p, c.Name, before, own)
			}
		}
		restore()
	}
}
