package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The smoke check is the only release gate that has run the released
// binary. Everything before it compares one version string to another,
// and all of those can pass on a release whose published wrapper
// downloads the wrong archive.

func TestCheckVersionOutputAcceptsExactlyWhatTheBinaryPrints(t *testing.T) {
	v := mustParseTag(t, "v0.2.8")
	for _, out := range []string{
		"jevlin 0.2.8",
		"jevlin 0.2.8\n",
		"jevlin 0.2.8\r\n",
		"  jevlin 0.2.8  \n",
	} {
		if err := CheckVersionOutput([]byte(out), v); err != nil {
			t.Errorf("%q was rejected: %v", out, err)
		}
	}
}

func TestCheckVersionOutputRejectsEverythingElse(t *testing.T) {
	v := mustParseTag(t, "v0.2.8")
	for _, tc := range []struct {
		out, why string
	}{
		{"", "nothing ran, or nothing was captured"},
		{"jevlin 0.2.7", "the previous release's binary — the failure the whole pipeline exists to catch"},
		{"jevlin v0.2.8", "goreleaser strips the v from -X main.version; expecting it here fails a correct release"},
		{"jevlin dev", "an un-stamped build"},
		{"jevlin dev (abc1234)", "an un-stamped build with a revision"},
		{"0.2.8", "a version with no binary name is not this binary"},
		{"jevlin 0.2.80", "a longer version must not satisfy a shorter one"},
		{"jevlin", "a name with no version"},
		{"jevlin 0.2.8 extra", "anything after the version means something else printed too"},
		{"jevlin installed for linux/amd64\njevlin 0.2.8", "the postinstall's own output captured along with the version"},
	} {
		if err := CheckVersionOutput([]byte(tc.out), v); err == nil {
			t.Errorf("%q was accepted: %s", tc.out, tc.why)
		}
	}
}

// The wrong-version rejection is the one this job exists for, so the
// message has to say which version was found as well as which was
// wanted — a bare "smoke failed" sends a human to three logs.
func TestCheckVersionOutputNamesBothVersions(t *testing.T) {
	err := CheckVersionOutput([]byte("jevlin 0.2.7"), mustParseTag(t, "v0.2.8"))
	if err == nil {
		t.Fatal("the previous release's version was accepted")
	}
	for _, want := range []string{"0.2.7", "0.2.8"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

func TestSmokeSubcommandReadsTheCapturedOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "version.txt")
	if err := os.WriteFile(path, []byte("jevlin 0.2.8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_OUTPUT", "")
	if err := runSmoke([]string{"-tag", "v0.2.8", "-file", path}, io.Discard); err != nil {
		t.Fatalf("the subcommand refused a correct smoke result: %v", err)
	}
	if err := runSmoke([]string{"-tag", "v0.2.9", "-file", path}, io.Discard); err == nil {
		t.Fatal("the subcommand accepted a binary reporting a different version than the tag")
	}
	if err := runSmoke([]string{"-tag", "v0.2.8", "-file", filepath.Join(dir, "absent.txt")}, io.Discard); err == nil {
		t.Fatal("a missing capture file was treated as a pass; a smoke test that ran nothing must not pass")
	}
}
