// Command testshard runs one part of a package set's tests, so a slow test
// run can be split across CI jobs that run at the same time.
//
// It is CI tooling and not part of the client: nothing in cmd/ or pkg/
// imports it, and .goreleaser.yaml builds ./cmd/jevlin and nothing else.
//
// The split is by test name. `go test -list` names every top-level test,
// example and fuzz target of each package; testshard deals those names, in
// the order they are listed, round-robin into -n parts, and runs part -i with
// `go test -run '^(name|name|...)$'`. Every listed name lands in exactly one
// part, so the parts together run every test the whole run would, and a test
// added later is dealt like the rest without anyone listing it.
//
// With -all it runs every part at once on this machine instead, which is
// how make race uses the cores a single test process leaves idle. Each
// part's output is held and printed whole when the part ends, so the parts'
// lines do not interleave.
//
// The deal is the decision with a right answer, so it lives in deal(), which
// a test drives; listing and running are thin calls to `go test`.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// maxPattern keeps one -run argument well under Linux's 128 KiB limit on a
// single argument. A part whose names do not fit needs more parts.
const maxPattern = 100_000

// entry is one listed test: the package it belongs to and its name.
type entry struct {
	pkg, name string
}

// testName is what `go test -list` prints for a runnable top-level test: a
// Go identifier, so any letter or digit, not only ASCII. Benchmarks are left
// out, because a plain `go test` does not run them, and so is the summary
// line. Any other line is refused rather than skipped: a name this pattern
// did not expect would otherwise be dropped from every part, and never run.
var (
	testName      = regexp.MustCompile(`^(Test|Example|Fuzz)[\p{L}\p{N}_]*$`)
	benchmarkName = regexp.MustCompile(`^Benchmark[\p{L}\p{N}_]*$`)
	summaryLine   = regexp.MustCompile(`^(ok|\?)\s`)
)

func main() {
	n := flag.Int("n", 1, "number of parts")
	i := flag.Int("i", 0, "the part to run, from 0 to n-1")
	all := flag.Bool("all", false, "run every part at once, on this machine")
	race := flag.Bool("race", false, "run with the race detector")
	flag.Parse()
	parts := []int{*i}
	if *all {
		parts = parts[:0]
		for k := 0; k < *n; k++ {
			parts = append(parts, k)
		}
	}
	if err := run(*n, parts, *race, flag.Args(), os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "testshard:", err)
		os.Exit(1)
	}
}

func run(n int, parts []int, race bool, pkgs []string, stdout, stderr io.Writer) error {
	// With no part to run, every check below would pass and nothing would
	// be tested, so a part count under one, or no part, is refused first.
	if n < 1 || len(parts) == 0 {
		return fmt.Errorf("%d parts: there must be at least one", n)
	}
	for _, i := range parts {
		if n < 1 || i < 0 || i >= n {
			return fmt.Errorf("part %d of %d does not exist", i, n)
		}
	}
	pkgs, err := expand(pkgs)
	if err != nil {
		return err
	}
	var flags []string
	if race {
		flags = append(flags, "-race")
	}
	var all []entry
	for _, pkg := range pkgs {
		names, err := list(pkg, flags)
		if err != nil {
			return err
		}
		for _, name := range names {
			all = append(all, entry{pkg, name})
		}
	}
	if len(parts) == 1 {
		return runPart(all, n, parts[0], pkgs, flags, stdout, stderr)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	failed := 0
	for _, i := range parts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var out bytes.Buffer
			err := runPart(all, n, i, pkgs, flags, &out, &out)
			mu.Lock()
			defer mu.Unlock()
			_, _ = stdout.Write(out.Bytes())
			if err != nil {
				fmt.Fprintln(stderr, "testshard:", err)
				failed++
			}
		}(i)
	}
	wg.Wait()
	if failed > 0 {
		return fmt.Errorf("%d of %d parts failed", failed, len(parts))
	}
	return nil
}

// runPart deals part i of n from every listed test and runs it, package by
// package.
func runPart(all []entry, n, i int, pkgs, flags []string, stdout, stderr io.Writer) error {
	part, err := deal(all, n, i)
	if err != nil {
		return err
	}
	failed := false
	for _, pkg := range pkgs {
		pattern, err := runPattern(part, pkg)
		if err != nil {
			return err
		}
		if pattern == "" {
			continue
		}
		args := append([]string{"test", "-count=1"}, flags...)
		args = append(args, "-run", pattern, pkg)
		fmt.Fprintf(stderr, "testshard: part %d of %d runs %d of the tests in %s\n", i, n, strings.Count(pattern, "|")+1, pkg)
		cmd := exec.Command("go", args...) // #nosec G204 -- CI tooling; the arguments are this run's own flags and listed test names
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Run(); err != nil {
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("part %d of %d failed", i, n)
	}
	return nil
}

// expand turns package patterns such as ./cmd/... into the packages they
// name, so each package's tests are listed and run in that package alone.
func expand(patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, fmt.Errorf("no packages named")
	}
	out, err := exec.Command("go", append([]string{"list"}, patterns...)...).Output() // #nosec G204 -- CI tooling; see run
	if err != nil {
		return nil, fmt.Errorf("listing the packages %v: %w", patterns, err)
	}
	pkgs := strings.Fields(string(out))
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("%v names no package", patterns)
	}
	return pkgs, nil
}

// list asks `go test -list` for the runnable tests of one package.
func list(pkg string, flags []string) ([]string, error) {
	args := append([]string{"test"}, flags...)
	args = append(args, "-list", ".", pkg)
	out, err := exec.Command("go", args...).Output() // #nosec G204 -- CI tooling; see run
	if err != nil {
		return nil, fmt.Errorf("listing the tests of %s: %w", pkg, err)
	}
	names, err := parseList(string(out))
	if err != nil {
		return nil, fmt.Errorf("listing the tests of %s: %w", pkg, err)
	}
	return names, nil
}

// parseList keeps the lines of `go test -list` output that name a runnable
// test, passes over the package summary and any benchmark, and refuses any
// other line.
func parseList(out string) ([]string, error) {
	var names []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || benchmarkName.MatchString(line) || summaryLine.MatchString(line):
		case testName.MatchString(line):
			names = append(names, line)
		default:
			return nil, fmt.Errorf("go test -list printed %q, which is not a test name this tool knows", line)
		}
	}
	return names, sc.Err()
}

// deal returns part i of n: every n-th listed test, starting at the i-th.
// Round-robin over the listed order spreads a file's tests, which are listed
// together and are often alike in cost, across every part. A part with no
// test is refused, because a job that runs nothing passes and would read as
// a pass of the tests it was meant to run.
func deal(all []entry, n, i int) ([]entry, error) {
	if len(all) == 0 {
		return nil, fmt.Errorf("no tests listed")
	}
	if n > len(all) {
		return nil, fmt.Errorf("%d parts for %d tests leaves a part empty", n, len(all))
	}
	var part []entry
	for k := i; k < len(all); k += n {
		part = append(part, all[k])
	}
	return part, nil
}

// runPattern is the -run argument that selects exactly the part's tests in
// pkg, or "" when the part has none there.
func runPattern(part []entry, pkg string) (string, error) {
	var names []string
	for _, e := range part {
		if e.pkg == pkg {
			names = append(names, e.name)
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	pattern := "^(" + strings.Join(names, "|") + ")$"
	if len(pattern) > maxPattern {
		return "", fmt.Errorf("the -run pattern for %s is %d bytes; split it into more parts", pkg, len(pattern))
	}
	return pattern, nil
}
