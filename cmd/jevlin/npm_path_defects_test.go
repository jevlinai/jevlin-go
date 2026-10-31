package main

// Each test here fails while the defect it names is present.

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Windows: an npm install that ran setup before this change has
// <home>\bin on its user Path, added by setup and journalled as such. Rerun
// with this change, setup's PATH directory becomes npm's, the journal's one
// PATH entry is overwritten, and uninstall can no longer take <home>\bin out.
// The functions are the ones the Windows environment step and uninstall
// call, driven over the map-backed user environment.
func TestWindowsRerunForgetsThePathEntryTheEarlierSetupAdded(t *testing.T) {
	for _, c := range []struct {
		name        string
		npmOnPathTo bool // the Node installer already put npm's directory on the user Path
	}{{"npm dir already on the user Path", true}, {"npm dir not on the user Path", false}} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			journal := filepath.Join(dir, setupEnvJournalFile)
			homeBin := `C:\Users\u\.jevlin\bin`
			npmDir := `C:\Users\u\AppData\Roaming\npm`
			cfg := `C:\Users\u\.jevlin\jevlin.toml`
			env := newFakeUserEnv()
			if c.npmOnPathTo {
				_ = env.Set("Path", `C:\Windows;`+npmDir)
			} else {
				_ = env.Set("Path", `C:\Windows`)
			}

			// The earlier setup (before it named npm's directory): binDir = <home>\bin.
			ch, err := planUserEnvironment(env, nil, homeBin, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := applyUserEnvironment(env, journal, ch); err != nil {
				t.Fatal(err)
			}
			// The current setup: binDir = npm's directory.
			prior, err := readEnvJournal(journal)
			if err != nil {
				t.Fatal(err)
			}
			ch, err = planUserEnvironment(env, prior, npmDir, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := applyUserEnvironment(env, journal, ch); err != nil {
				t.Fatal(err)
			}
			// Uninstall.
			j, err := readEnvJournal(journal)
			if err != nil || j == nil {
				t.Fatalf("journal: %v %v", j, err)
			}
			plan, err := planUserEnvironmentRevert(env, *j)
			if err != nil {
				t.Fatal(err)
			}
			if err := applyUserEnvironmentRevert(env, journal, plan); err != nil {
				t.Fatal(err)
			}
			path, _, _ := env.Get("Path")
			if pathHasEntry(path, homeBin) {
				t.Errorf("after uninstall the user Path still holds %s, which setup added: Path=%q (journal had %+v)", homeBin, path, j.Path)
			}
		})
	}
}

// The doctor row for an npm install is gathered only when the launcher
// marked the process. The agents run the package binary by its full path
// (no JEVLIN_LAUNCH), and an npm install keeps no <home>/bin binary, so a
// doctor run that way reports nothing about PATH at all.
func TestDoctorRunByThePackageBinarysPathSaysNothingAboutPath(t *testing.T) {
	pkgExe := "/opt/npmprefix/lib/node_modules/@jevlinsearch/jevlin/bin/jevlin"
	notFound := func(string) (string, error) { return "", exec.ErrNotFound }
	f := doctorFacts{
		NpmShim: gatherNpmShimFacts(pkgExe, "darwin", func(string) string { return "" }, notFound),
		Binary:  gatherBinaryFacts(t.TempDir()), // an npm install's home: no bin/jevlin
	}
	for _, c := range assembleDoctor(f) {
		if c.Name == "npm command on PATH" || c.Name == doctorBinaryCheck(binaryFacts{Checked: true}).Name {
			return
		}
	}
	t.Errorf("doctor run as the agents run it (the package binary by path) has no PATH row for an npm install; npm facts: %+v", f.NpmShim)
}

// uninstall attributes a block by its JEVLIN_CONFIG value, accepting the
// fish spelling for a POSIX `export` line and vice versa. With a backslash in
// a path the two spellings collide, so one installation's uninstall claims
// another's block.
func TestAPosixBlockIsNotClaimedThroughTheFishSpelling(t *testing.T) {
	other := `/h/a\\b/jevlin.toml` // another installation's config, written by a POSIX block
	mine := `/h/a\b/jevlin.toml`
	block := envLinesFor(dialectPOSIX, "/opt/npm/bin", other)
	if profileBlockNamesConfig(block, mine) {
		t.Errorf("a POSIX block exporting %s is attributed to %s: %q", other, mine, block)
	}
}

// for a native install the POSIX block is
// byte-for-byte what setup wrote before shells were told apart (copied here).
func TestSoundNativePosixLinesUnchanged(t *testing.T) {
	mainLines := func(binDir, cfgPath string) []string {
		q := shellQuote(binDir)
		return []string{
			`case ":$PATH:" in *:` + q + `:*) ;; *) export PATH="$PATH:"` + q + ` ;; esac`,
			"export JEVLIN_CONFIG=" + shellQuote(cfgPath),
		}
	}
	for _, d := range []string{"/home/u/.jevlin/bin", "/home/Jane Doe/.jevlin/bin", "/h/it's/bin", "/h/$x/`y`/bin"} {
		cfg := filepath.Join(filepath.Dir(d), "jevlin.toml")
		if a, b := profileBlock(envLinesFor(dialectPOSIX, d, cfg)), profileBlock(mainLines(d, cfg)); a != b {
			t.Errorf("%s:\n%s\nvs main:\n%s", d, a, b)
		}
	}
}

// When npm's directory cannot be told, setup says it writes no PATH line and
// names nothing to put on PATH.
func TestNoPathDirectoryWhenNpmDirIsUnknown(t *testing.T) {
	r := &setupRun{exe: `D:\tools\jevlin-pkg\bin\jevlin.exe`, d: setupDeps{getenv: func(k string) string {
		if k == "JEVLIN_LAUNCH" {
			return "npm:global"
		}
		return ""
	}}}
	dir, note := r.pathDirectory(`C:\Users\u\.jevlin\bin`)
	if dir != "" || !strings.Contains(note, "writes no PATH line") {
		t.Errorf("dir=%q note=%q, want no directory and a note that no PATH line is written", dir, note)
	}
}
