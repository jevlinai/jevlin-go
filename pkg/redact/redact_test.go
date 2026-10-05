package redact

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
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

// A password has no shape, so the name is what gives it away: an
// assignment whose name has a secret word as a whole segment loses its
// value, quoted or not, and keeps its name.
func TestSecretNamedAssignmentsLoseTheirValue(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"env style":          {"DATABASE_PASSWORD=hunter2", "DATABASE_PASSWORD=" + placeholder},
		"export":             {"run export API_TOKEN=abc123 first", "run export API_TOKEN=" + placeholder + " first"},
		"double quoted":      {`CLIENT_SECRET="two words"`, "CLIENT_SECRET=" + placeholder},
		"single quoted":      {`DB_PASSWD='p w'`, "DB_PASSWD=" + placeholder},
		"lowercase name":     {"password=hunter2", "password=" + placeholder},
		"flag":               {"mysql --password=hunter2 -h db", "mysql --password=" + placeholder + " -h db"},
		"upper KEY":          {"STRIPE_KEY=whsec_1", "STRIPE_KEY=" + placeholder},
		"upper PASS":         {"DB_PASS=x9", "DB_PASS=" + placeholder},
		"two on a line":      {"A_TOKEN=one B_SECRET=two", "A_TOKEN=" + placeholder + " B_SECRET=" + placeholder},
		"after a newline":    {"first\nAPI_TOKEN=abc", "first\nAPI_TOKEN=" + placeholder},
		"powershell":         {`$env:API_TOKEN='abc'`, `$env:API_TOKEN=` + placeholder},
		"already redacted":   {"API_TOKEN=" + placeholder, "API_TOKEN=" + placeholder},
		"credentials plural": {"AWS_CREDENTIALS=a:b", "AWS_CREDENTIALS=" + placeholder},
		"camel case apikey":  {"?apiKey=abc123", "?apiKey=" + placeholder},
	} {
		if got := TraceText(tc.in); got != tc.want {
			t.Errorf("%s: TraceText(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
	}
}

// The rule reads a whole segment of the name, and KEY and PASS only in an
// all-uppercase name, so ordinary text about keys, passes and tokens is
// not corpus damage.
func TestAssignmentsThatAreNotSecretsSurvive(t *testing.T) {
	for name, text := range map[string]string{ // #nosec G101 -- prose that must NOT be read as credentials
		"key=value prose":      "pass key=value pairs to the parser",
		"sort flag":            "sort --key=2 file.txt",
		"lowercase pass":       "the compiler runs pass=2 next",
		"substring, not a seg": "MONKEY=banana TOKENIZER_PATH=/opt/tok",
		"path variables":       "JEVLIN_CONFIG=/etc/jevlin.toml JEVLIN_HOME=/opt/jevlin",
		"pwd":                  "PWD=/srv/app OLDPWD=/srv",
		"no value":             "set API_TOKEN= to clear it",
		"comparison":           "if token == expected then",
	} {
		if got := TraceText(text); got != text {
			t.Errorf("%s: TraceText altered non-secret text:\n  in:  %q\n  out: %q", name, text, got)
		}
	}
}

// Five or more NAME=value lines in a row is an environment dump: every
// value goes and every name stays. Four is still prose about settings.
func TestAnEnvironmentDumpLosesEveryValue(t *testing.T) {
	dump := "here is the env:\nSHELL=/bin/zsh\nLANG=en_US.UTF-8\nexport EDITOR=vim\nTERM_PROGRAM=iTerm.app\n  COLORTERM=truecolor\r\nMYAPP_REGION=eu-west-9\nthat is all"
	got := TraceText(dump)
	for _, value := range []string{"/bin/zsh", "en_US.UTF-8", "vim", "iTerm.app", "truecolor", "eu-west-9"} {
		if strings.Contains(got, value) {
			t.Errorf("an environment dump kept the value %q: %q", value, got)
		}
	}
	for _, kept := range []string{"here is the env:", "SHELL=", "export EDITOR=", "  COLORTERM=" + placeholder + "\r\n", "MYAPP_REGION=", "that is all"} {
		if !strings.Contains(got, kept) {
			t.Errorf("an environment dump lost %q: %q", kept, got)
		}
	}

	four := "SHELL=/bin/zsh\nLANG=en_US.UTF-8\nEDITOR=vim\nTERM_PROGRAM=iTerm.app\nand then prose"
	if got := TraceText(four); got != four {
		t.Errorf("four assignment lines were treated as a dump: %q", got)
	}
	broken := "A=1\nB=2\nprose in between\nC=3\nD=4\nE=5"
	if got := TraceText(broken); got != broken {
		t.Errorf("a run broken by prose was treated as a dump: %q", got)
	}
}

// The bridge variable's value is an encoded envelope that can hold earlier
// assistant text. No pattern can see into it, so it goes whatever it is.
func TestTheTraceBridgeAssignmentLosesItsValue(t *testing.T) {
	for _, text := range []string{
		"JEVLIN_TRACE_BRIDGE=eyJ2IjoxfQ jevlin search --stdin",
		`$env:JEVLIN_TRACE_BRIDGE='eyJ2IjoxfQ'; jevlin search --stdin`,
		`set "JEVLIN_TRACE_BRIDGE=eyJ2IjoxfQ" && jevlin search`,
	} {
		got := TraceText(text)
		if strings.Contains(got, "eyJ2IjoxfQ") || !strings.Contains(got, traceBridgeEnvName+"="+placeholder) {
			t.Errorf("the bridge value survived: %q -> %q", text, got)
		}
		if !strings.Contains(got, "jevlin search") {
			t.Errorf("more than the bridge value was removed: %q -> %q", text, got)
		}
	}
}

// The hostname's first label and the account name go wherever they stand
// as a whole word, in any letter case, and nowhere else.
func TestTheLocalHostAndAccountNamesAreRemoved(t *testing.T) {
	defer SetLocalIdentity("Build-Box7.corp.example", `CORP\mwhitlock`)()
	for name, tc := range map[string]struct{ in, want string }{
		"USER":            {"USER=mwhitlock", "USER=" + placeholder},
		"ssh target":      {"ssh mwhitlock@db1 uptime", "ssh " + placeholder + "@db1 uptime"},
		"prose":           {"logged in as mwhitlock on build-box7.", "logged in as " + placeholder + " on " + placeholder + "."},
		"prompt":          {"mwhitlock@Build-Box7:~$ ls", placeholder + "@" + placeholder + ":~$ ls"},
		"upper case":      {"HOSTNAME is BUILD-BOX7", "HOSTNAME is " + placeholder},
		"fqdn keeps rest": {"host build-box7.corp.example is up", "host " + placeholder + ".corp.example is up"},
		"twice":           {"mwhitlock mwhitlock", placeholder + " " + placeholder},
	} {
		if got := TraceText(tc.in); got != tc.want {
			t.Errorf("%s: TraceText(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
	}
	for name, text := range map[string]string{
		"inside a longer word": "the mwhitlocks and xmwhitlock and mwhitlock_2",
		"inside a longer host": "prebuild-box7x is another machine",
	} {
		if got := TraceText(text); got != text {
			t.Errorf("%s: a name inside a longer word was removed: %q -> %q", name, text, got)
		}
	}
}

// A generic or very short name identifies nobody and is an ordinary word:
// replacing it everywhere would destroy the text to hide nothing.
func TestGenericLocalNamesAreNotSearchedFor(t *testing.T) {
	text := "the user asked the admin to restart the server as root on localhost; id ab"
	for _, id := range [][2]string{{"localhost", "user"}, {"server.local", "root"}, {"ubuntu", "admin"}, {"ab", "ab"}, {"", ""}, {"héllo-box", "josé"}} {
		restore := SetLocalIdentity(id[0], id[1])
		if got := TraceText(text); got != text {
			t.Errorf("identity %q/%q: generic words were removed: %q", id[0], id[1], got)
		}
		restore()
	}
}

// An occurrence that overlaps a rejected one is still found, as the
// JavaScript scrubber's lookarounds find it.
func TestAnOverlappingNameIsStillFound(t *testing.T) {
	defer SetLocalIdentity("a-a", "")()
	if got, want := TraceText("xa-a-a"), "xa-"+placeholder; got != want {
		t.Errorf("TraceText = %q, want %q", got, want)
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
