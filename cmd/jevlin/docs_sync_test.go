package main

// Documentation that has to agree with other documentation, checked the
// way code is.
//
// npm/README.md is not a summary of README.md, or a copy someone remembers
// to refresh — it IS README.md, plus the one section that only makes sense
// on npm, with one section of its own: how to install it. It had drifted by five releases before anyone noticed, because
// nothing was watching: the page npm serves described a binary that no
// longer existed, while the repository's own front page was current. A
// prose rule ("keep them in sync") is the thing that failed. Byte equality
// is the thing that cannot.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// npmWrapperSuffix is the ONLY text npm/README.md may carry beyond the
// top-level README, appended verbatim after it. It describes the npm
// package itself — the download, the checksum check, the two escape
// hatches — which is true of the wrapper and of nothing else in the
// repository, so it has no place on the front page.
//
// Held here as a constant, not read out of npm/README.md, on purpose: a
// suffix derived from the file under test would make this assertion
// unfalsifiable. Changing the wrapper section means changing this
// constant and the file together, which is the review the section is
// owed.
const npmWrapperSuffix = "\n## The npm wrapper\n\n" +
	"This package downloads the release binary for your platform on install, " +
	"checks the release's signature and verifies the binary against its checksums, " +
	"and forwards every argument to it. " +
	"The package version is the release tag it fetches.\n\n" +
	"- `JEVLIN_BINARY=/path` use a binary already on the machine\n" +
	"- `JEVLIN_SKIP_DOWNLOAD=1` install the wrapper without fetching\n"

// installHeading starts the one section npm/README.md words for itself, up
// to the next "## " heading.
const installHeading = "## How do I install it?"

// npmInstallSection is npm/README.md's own install section, held here byte
// for byte. npm's page is read by someone who already has npm, so it leads
// with the npm install; GitHub's front page leads with the one-line
// installers. Every other byte of npm/README.md is README.md's. Held as a
// constant, as npmWrapperSuffix is, so a change to npm's install section
// is a change to this test, and so a review, while the root README's own
// install section may change freely.
const npmInstallSection = "## How do I install it?\n" +
	"```bash\n" +
	"npm install -g @jevlinsearch/jevlin\n" +
	"jevlin setup\n" +
	"```\n" +
	"Install it globally, not with `npx`: setup writes the binary's path into your agents' skills and\n" +
	"hooks, and npm throws an `npx` cache away.\n" +
	"\n" +
	"```bash\n" +
	"curl -fsSL https://jevlin.ai/install.sh | sh     # macOS, Linux and WSL\n" +
	"\n" +
	"irm https://jevlin.ai/install.ps1 | iex          # Windows PowerShell\n" +
	"```\n" +
	"This installs the latest release at `~/.jevlin/bin/jevlin` (on Windows,\n" +
	"`%USERPROFILE%\\.jevlin\\bin\\jevlin.exe`), checked against the release's checksums and, where a tool\n" +
	"on the machine can, its signature, and runs setup when you ran it at a terminal. From CMD,\n" +
	"`install.cmd` runs the PowerShell installer. `JEVLIN_HOME` installs it elsewhere and\n" +
	"`JEVLIN_NO_SETUP=1` stops before setup ([options](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#installing-without-npm)).\n" +
	"\n" +
	"It runs on macOS 13 or later, Linux and Windows, each on amd64 and arm64.\n" +
	"\n" +
	"### Can I install it another way?\n" +
	"\n" +
	"- **From GitHub Releases:** download the archive for your platform and check it against its\n" +
	"  `checksums.txt` ([steps](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#installing-without-npm))\n" +
	"\n" +
	"- **From an agent marketplace:** Claude Code's and Codex's plugin marketplaces, and skills.sh,\n" +
	"  carry a setup skill that installs and sets up jevlin. With skills.sh that is\n" +
	"  `npx skills add jevlinai/jevlin-go` ([the others](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#let-your-agent-install-it))\n" +
	"\n" +
	"- **From source:** `go install github.com/jevlinai/jevlin-go/cmd/jevlin@latest`, or `make build`\n" +
	"  in a clone. It reports its version as `dev`, so `jevlin upgrade` and the daily check leave it\n" +
	"  alone ([details](https://github.com/jevlinai/jevlin-go/blob/main/docs/reference.md#building-from-source))\n" +
	"\n" +
	"**`jevlin: command not found`?** Run setup by its full path. [The guide shows how](https://github.com/jevlinai/jevlin-go/blob/main/docs/guide.md#jevlin-command-not-found).\n" +
	"\n"

// installSectionBounds is where the install section runs in a README: from
// its heading up to the next "## " heading. ok is false when there is no
// such section.
func installSectionBounds(b []byte) (start, end int, ok bool) {
	start = bytes.Index(b, []byte(installHeading))
	if start < 0 {
		return 0, 0, false
	}
	next := bytes.Index(b[start+len(installHeading):], []byte("\n## "))
	if next < 0 {
		return 0, 0, false
	}
	return start, start + len(installHeading) + next + 1, true
}

func TestNPMReadmeMirrorsRootREADME(t *testing.T) {
	root := moduleRoot(t)

	top, err := os.ReadFile(filepath.Join(root, "README.md")) // #nosec G304 -- a fixed path under this module's own root
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	npm, err := os.ReadFile(filepath.Join(root, "npm", "README.md")) // #nosec G304 -- a fixed path under this module's own root
	if err != nil {
		t.Fatalf("read npm/README.md: %v", err)
	}

	start, end, ok := installSectionBounds(top)
	if !ok {
		t.Fatalf("README.md has no %q section ending at a following \"## \" heading", installHeading)
	}
	want := make([]byte, 0, len(top)+len(npmInstallSection)+len(npmWrapperSuffix))
	want = append(want, top[:start]...)
	want = append(want, npmInstallSection...)
	want = append(want, top[end:]...)
	want = append(want, npmWrapperSuffix...)

	if bytes.Equal(npm, want) {
		return
	}

	// Say where, not just that. A diff of two 16 KB files in a test log is
	// unreadable; the first differing byte, with its line number and both
	// sides' context, is what a person actually needs.
	line, col, gotCtx, wantCtx := firstDifference(npm, want)
	t.Fatalf("npm/README.md is not README.md, with npmInstallSection for its install section, + the wrapper suffix.\n"+
		"first difference at line %d, column %d:\n"+
		"  npm/README.md:  %q\n"+
		"  expected:       %q\n"+
		"(npm/README.md is %d bytes, expected %d)\n"+
		"Fix by regenerating it: the top-level README with npmInstallSection in place of its install section, then npmWrapperSuffix; a change to npm's install section changes npmInstallSection too.",
		line, col, gotCtx, wantCtx, len(npm), len(want))
}

// npm/NOTICE is the root NOTICE, byte for byte. npm packs only what lies
// inside npm/, so the package cannot reach the root file and carries a copy;
// the Apache License asks that a redistribution carry the NOTICE, and a copy
// that has drifted carries the wrong one. Same argument as the README above:
// byte equality, not a rule to remember.
func TestNPMNoticeMirrorsRootNOTICE(t *testing.T) {
	root := moduleRoot(t)

	top, err := os.ReadFile(filepath.Join(root, "NOTICE")) // #nosec G304 -- a fixed path under this module's own root
	if err != nil {
		t.Fatalf("read NOTICE: %v", err)
	}
	npm, err := os.ReadFile(filepath.Join(root, "npm", "NOTICE")) // #nosec G304 -- a fixed path under this module's own root
	if err != nil {
		t.Fatalf("read npm/NOTICE: %v", err)
	}
	if bytes.Equal(npm, top) {
		return
	}
	line, col, gotCtx, wantCtx := firstDifference(npm, top)
	t.Fatalf("npm/NOTICE is not a copy of NOTICE.\n"+
		"first difference at line %d, column %d:\n"+
		"  npm/NOTICE:  %q\n"+
		"  NOTICE:      %q\n"+
		"Fix by copying NOTICE over npm/NOTICE.",
		line, col, gotCtx, wantCtx)
}

// firstDifference locates the first byte at which got and want diverge and
// returns its 1-based line and column plus a bounded excerpt of each side
// from that point.
func firstDifference(got, want []byte) (line, col int, gotCtx, wantCtx string) {
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	line, col = 1, 1
	for _, b := range got[:i] {
		if b == '\n' {
			line++
			col = 1
			continue
		}
		col++
	}
	return line, col, excerptFrom(got, i), excerptFrom(want, i)
}

func excerptFrom(b []byte, i int) string {
	const window = 60
	if i >= len(b) {
		return "<end of file>"
	}
	end := i + window
	if end > len(b) {
		end = len(b)
	}
	return string(b[i:end])
}
