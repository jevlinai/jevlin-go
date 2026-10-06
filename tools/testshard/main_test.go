package main

import (
	"fmt"
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
			sizes := map[int]bool{}
			for i := 0; i < n; i++ {
				part, err := deal(all, n, i)
				if err != nil {
					t.Fatalf("%d tests, part %d of %d: %v", total, i, n, err)
				}
				sizes[len(part)] = true
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
			if len(sizes) > 2 {
				t.Errorf("%d tests in %d parts: part sizes %v differ by more than one", total, n, sizes)
			}
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
func TestTheListKeepsOnlyRunnableTests(t *testing.T) {
	out := "TestOne\nTestTwo_sub\nBenchmarkSlow\nExampleThing\nFuzzParse\n" +
		"ok  \tgithub.com/x/y\t0.012s\n"
	got := strings.Join(parseList(out), ",")
	if want := "TestOne,TestTwo_sub,ExampleThing,FuzzParse"; got != want {
		t.Errorf("parseList = %s, want %s", got, want)
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
