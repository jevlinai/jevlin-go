package main

import (
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/internal/selfupdate"
)

// The daily check installs a patch release of this copy's own minor, and
// leaves a release of another minor to a person (autoAppliesAcross).
func TestTheDailyCheckCrossesNoMinorReleaseOnItsOwn(t *testing.T) {
	v := func(s string) selfupdate.Version {
		t.Helper()
		x, err := selfupdate.ParseVersion(s)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	for _, c := range []struct {
		from, to string
		want     bool
	}{
		{"0.7.0", "0.7.1", true},
		{"0.7.3", "0.7.10", true},
		{"0.7.9", "0.8.0", false},
		{"0.7.0", "1.7.0", false},
		{"1.2.3", "1.3.0", false},
	} {
		if got := autoAppliesAcross(v(c.from), v(c.to)); got != c.want {
			t.Errorf("%s -> %s: %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

// End to end: a new minor release is looked up once, recorded as available
// with status saying why it was left, not looked up again the same day, and
// installed by a person's `jevlin upgrade`.
func TestANewMinorReleaseIsLeftToAPersonAndSaidSo(t *testing.T) {
	f, cfgPath, stamp := autoFixture(t, newLocalRelease(t, "0.4.0", map[string]string{"0.4.0": "0.4.0\n"}), "")
	if code, out, errOut := f.runAuto(cfgPath); code != exitOK || f.content(f.exe) != "0.3.0" {
		t.Fatalf("the check crossed a minor release: exit %d, binary %s\n%s\n%s", code, f.content(f.exe), out, errOut)
	}
	st := readStampT(t, stamp)
	if st.Outcome != updateAvailable || st.Latest != "0.4.0" || st.Running != "0.3.0" {
		t.Fatalf("the check's record %+v", st)
	}
	withBuild(t, "0.3.0")
	if line := updateLineText(factsFor(t, f, cfgPath), time.Now()); !strings.Contains(line, "0.4.0 is available, a new minor release, which is not installed automatically") {
		t.Errorf("status on the copy the check left: %q", line)
	}
	// Once a day: a second check the same day looks nothing up.
	lookups := len(f.lr.requests)
	if code, _, _ := f.runAuto(cfgPath); code != exitOK || len(f.lr.requests) != lookups {
		t.Errorf("a second check the same day: exit %d, %d request(s) more", code, len(f.lr.requests)-lookups)
	}
	// A person installs it.
	if code, out, errOut := f.run(); code != exitOK || f.content(f.exe) != "0.4.0" {
		t.Fatalf("the person's upgrade: exit %d, binary %s\n%s\n%s", code, f.content(f.exe), out, errOut)
	}
}
