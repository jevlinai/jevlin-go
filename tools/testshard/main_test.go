package main

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
)

// The parts together are every listed test, each exactly once, for every
// split that leaves no part empty. A test that fell between parts would
// never run in CI and nothing would say so.
func TestThePartsAreEveryTestExactlyOnce(t *testing.T) {
	for _, total := range []int{1, 2, 7, 100, 1001} {
		var all []entry
		for k := 0; k < total; k++ {
			all = append(all, entry{fmt.Sprintf("p%d", k%3), fmt.Sprintf("Test%d", k)})
		}
		for n := 1; n <= total && n <= 9; n++ {
			seen := map[entry]int{}
			smallest, largest := total, 0
			for i := 0; i < n; i++ {
				part, err := deal(all, n, i)
				if err != nil {
					t.Fatalf("%d tests, part %d of %d: %v", total, i, n, err)
				}
				smallest, largest = min(smallest, len(part)), max(largest, len(part))
				for _, e := range part {
					seen[e]++
				}
			}
			if len(seen) != total {
				t.Errorf("%d tests in %d parts: %d dealt", total, n, len(seen))
			}
			for e, c := range seen {
				if c != 1 {
					t.Errorf("%d tests in %d parts: %v dealt %d times", total, n, e, c)
				}
			}
			if largest-smallest > 1 {
				t.Errorf("%d tests in %d parts: part sizes run from %d to %d", total, n, smallest, largest)
			}
		}
	}
}

// A run with no part to run is refused before anything is listed: it would
// pass having tested nothing, as make race RACE_PARTS=0 once did.
func TestARunWithNoPartIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		n     int
		parts []int
	}{
		"zero parts":            {0, nil},
		"no part named":         {3, nil},
		"a part past the count": {3, []int{3}},
	} {
		if err := run(c.n, c.parts, false, []string{"./..."}, io.Discard, io.Discard); err == nil {
			t.Errorf("%s: the run was accepted", name)
		}
	}
}

// A part that would run nothing is refused: its job would pass.
func TestAnEmptyPartIsRefused(t *testing.T) {
	if _, err := deal(nil, 1, 0); err == nil {
		t.Error("nothing listed, yet a part was dealt")
	}
	two := []entry{{"p", "TestA"}, {"p", "TestB"}}
	if _, err := deal(two, 3, 2); err == nil {
		t.Error("three parts of two tests were dealt")
	}
}

// What `go test -list` prints, benchmarks and the package summary included.
// A test name is a Go identifier, so a name with a non-ASCII letter is kept.
func TestTheListKeepsOnlyRunnableTests(t *testing.T) {
	out := "TestOne\nTestTwo_sub\nBenchmarkSlow\nExampleThing\nFuzzParse\nTest\u00c9t\u00e9\n" +
		"ok  \tgithub.com/x/y\t0.012s\n"
	names, err := parseList(out)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(names, ","), "TestOne,TestTwo_sub,ExampleThing,FuzzParse,Test\u00c9t\u00e9"; got != want {
		t.Errorf("parseList = %s, want %s", got, want)
	}
	if names, err := parseList("?   \tgithub.com/x/z\t[no test files]\n"); err != nil || len(names) != 0 {
		t.Errorf("a package with no tests: %v, %v", names, err)
	}
}

// A line the list is not expected to hold is refused, not skipped: a test
// whose name it is would otherwise be in no part, and never run.
func TestAnUnknownListLineIsRefused(t *testing.T) {
	if _, err := parseList("TestOne\nsomething-else\nok  \tgithub.com/x/y\t0.01s\n"); err == nil {
		t.Error("an unknown line was skipped")
	}
}

// The pattern selects the part's names in that package and nothing else:
// no other test of the package, and no test whose name merely starts with
// one of them.
func TestThePatternSelectsExactlyThePart(t *testing.T) {
	part := []entry{{"p", "TestA"}, {"q", "TestB"}, {"p", "TestC"}}
	pattern, err := runPattern(part, "p")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(pattern)
	for name, want := range map[string]bool{"TestA": true, "TestC": true, "TestB": false, "TestAB": false, "XTestA": false} {
		if re.MatchString(name) != want {
			t.Errorf("pattern %q matches %s: %v, want %v", pattern, name, !want, want)
		}
	}
	if p, _ := runPattern(part, "r"); p != "" {
		t.Errorf("a package with none of the part's tests got pattern %q", p)
	}
}

// A pattern too long to pass as one argument is refused before it is run.
func TestAPatternTooLongForOneArgumentIsRefused(t *testing.T) {
	var part []entry
	for k := 0; len(part)*12 < maxPattern+1000; k++ {
		part = append(part, entry{"p", fmt.Sprintf("TestLong%04d", k)})
	}
	if _, err := runPattern(part, "p"); err == nil {
		t.Error("a pattern over the limit was accepted")
	}
}
