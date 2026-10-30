package main

import (
	"bytes"
	"os"
	"testing"
)

// statusText is the text status report for cfgPath, stdout and stderr
// together, as a participant would read it.
func statusText(t *testing.T, cfgPath string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	statusMain([]string{"-config", cfgPath}, &stdout, &stderr, os.Getenv)
	return stdout.String() + stderr.String()
}
