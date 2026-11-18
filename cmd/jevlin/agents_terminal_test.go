package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The terminal test agents uses is the descriptor's, as setup's is: a
// stdin redirected from /dev/null (a character device, like NUL on
// Windows) is not a terminal, and neither is a pipe. The real function
// reads the process's own stdin, so each case runs this test binary again
// with that stdin and reads the answer it prints.
func TestStdinFromTheNullDeviceOrAPipeIsNotATerminal(t *testing.T) {
	if os.Getenv("JEVLIN_TEST_PRINT_STDIN_IS_TERMINAL") == "1" {
		fmt.Println(stdinIsTerminal())
		return
	}
	for _, c := range []struct {
		name  string
		stdin func(t *testing.T) io.Reader
	}{
		{"null device", func(t *testing.T) io.Reader {
			f, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.Close() })
			return f
		}},
		{"pipe", func(t *testing.T) io.Reader {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			t.Cleanup(func() { _ = r.Close() })
			return r
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestStdinFromTheNullDeviceOrAPipeIsNotATerminal$")
			cmd.Env = append(os.Environ(), "JEVLIN_TEST_PRINT_STDIN_IS_TERMINAL=1")
			cmd.Stdin = c.stdin(t)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("running the test binary again: %v\n%s", err, out)
			}
			first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
			if first != "false" {
				t.Fatalf("stdin from a %s was taken for a terminal: %q", c.name, out)
			}
		})
	}
}
