package redact

import (
	"errors"
	"os"
	"regexp"
	"sort"
	"strconv"
	"testing"
)

// The account is read from the environment and then the home directory,
// in the order the JavaScript scrubber reads it, and the user database is
// never asked.
func TestTheAccountIsFoundWithoutAskingTheUserDatabase(t *testing.T) {
	home := func(dir string, err error) func() (string, error) {
		return func() (string, error) { return dir, err }
	}
	for name, tc := range map[string]struct {
		goos string
		env  map[string]string
		home func() (string, error)
		want string
	}{
		"USER first":                {"linux", map[string]string{"USER": "ann", "LOGNAME": "bob"}, home("/home/cid", nil), "ann"},
		"then LOGNAME":              {"darwin", map[string]string{"LOGNAME": "bob"}, home("/Users/cid", nil), "bob"},
		"then the home directory":   {"linux", nil, home("/home/cid/", nil), "cid"},
		"windows reads USERNAME":    {"windows", map[string]string{"USERNAME": "John Smith", "USER": "ann"}, home(`C:\Users\cid`, nil), "John Smith"},
		"windows ignores USER":      {"windows", map[string]string{"USER": "ann"}, home("/home/cid", nil), "cid"},
		"nothing at all":            {"linux", nil, home("", errors.New("no home")), ""},
		"an empty variable is none": {"linux", map[string]string{"USER": ""}, home("/home/cid", nil), "cid"},
	} {
		getenv := func(k string) string { return tc.env[k] }
		if got := localAccount(tc.goos, getenv, tc.home); got != tc.want {
			t.Errorf("%s: localAccount = %q, want %q", name, got, tc.want)
		}
	}
}

// The JavaScript scrubber keeps its own copies of the word lists and the
// numbers; the shared table catches a word it lacks only when a row
// happens to use it. This reads them out of the shared source and holds
// them to Go's, whole.
func TestTheJavaScriptListsAreGosLists(t *testing.T) {
	raw, err := os.ReadFile("../../cmd/jevlin/agent_trace_common.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	set := func(name string) []string {
		t.Helper()
		m := regexp.MustCompile(`(?s)const ` + name + ` = new Set\(\[(.*?)\]\)`).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("the shared source defines no set %s", name)
		}
		var out []string
		for _, w := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[1], -1) {
			out = append(out, w[1])
		}
		sort.Strings(out)
		return out
	}
	keys := func(m map[string]bool) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	for js, goList := range map[string]map[string]bool{
		"TRACE_GENERIC_IDENTITY":          genericIdentityNames,
		"TRACE_SECRET_SEGMENTS":           secretNameSegments,
		"TRACE_SECRET_SEGMENTS_QUALIFIED": secretNameSegmentsQualified,
		"TRACE_SECRET_NAMES":              secretWholeNames,
		"TRACE_ACCOUNT_VARIABLES":         accountVariables,
	} {
		if got, want := set(js), keys(goList); !equalStrings(got, want) {
			t.Errorf("%s differs from Go's list\n  js: %q\n  go: %q", js, got, want)
		}
	}
	for js, want := range map[string]int{
		"TRACE_QUOTED_VALUE_MAX_LINES": quotedValueMaxLines,
		"TRACE_ENV_DUMP_RUN":           envDumpRun,
	} {
		m := regexp.MustCompile(`const ` + js + ` = ([0-9]+)\n`).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("the shared source defines no number %s", js)
		}
		if got, _ := strconv.Atoi(m[1]); got != want {
			t.Errorf("%s is %d in the shared source and %d in Go", js, got, want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
