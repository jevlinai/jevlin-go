package main

// dropin-miner#99: our block is written where it is found, and no byte outside our
// markers moves.
//
// The block used to be stripped and appended on every write, so a file's
// order changed for a change that was only ever to our own table: the
// participant's [projects] and [windows] ended up above a block that had
// been below them, and "nothing else changed" stopped being checkable by
// comparing bytes.
//
// The permission profile adds one rule: the region carries a bare key,
// default_permissions, which TOML reads as part of whatever table precedes
// it. So the region's place is before the file's first table header. Found
// there, it is written where it is; found below a header — where its key is
// already the wrong table's, and Codex refuses the file — it is moved up,
// and still no line of the participant's moves relative to another.

import (
	"reflect"
	"strings"
	"testing"
)

const codexConfigPath = "/home/u/.codex/config.toml"

// outsideOurMarkers is everything in a Codex config that is not ours: the
// bytes before the begin marker and the bytes after the end marker. That
// pair is the guarantee this file exists for -- a participant auditing a
// machine is making a claim about these bytes, not about ours.
func outsideOurMarkers(t *testing.T, file string) (pre, post string) {
	t.Helper()
	pre, _, post, ok := markedRegion([]byte(file))
	if !ok {
		t.Fatalf("no jevlin block in:\n%s", file)
	}
	return pre, post
}

// participantLines is every line of the file that is not inside our markers,
// blank lines dropped. Its ORDER is what an uninstall-and-install round trip
// must preserve even where the block itself cannot go back to where it was.
func participantLines(t *testing.T, file string) []string {
	t.Helper()
	var out []string
	inside := false
	for _, l := range strings.Split(file, "\n") {
		switch {
		case strings.Contains(l, agentsMarkerBegin):
			inside = true
		case strings.Contains(l, agentsMarkerEnd):
			inside = false
		case inside:
		case strings.TrimSpace(l) == "":
		default:
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return out
}

// staleOurTable makes our own table differ from what the renderer would
// write now, so an install has something to do. It changes a value INSIDE
// our markers and nothing else: one allowed host, as a router_url edit
// would leave it.
func staleOurTable(t *testing.T, file string) string {
	t.Helper()
	stale := strings.Replace(file, refreshedHost, `"stale.example.invalid" = "allow"`, 1)
	if stale == file {
		t.Fatal("this fixture is meant to make our own table stale and did not change a byte, so the install below would have nothing to write")
	}
	return stale
}

// refreshedHost is the line a refresh of the stale fixture writes back.
const refreshedHost = `"router.example.invalid" = "allow"`

// A reinstall rewrites our block where it stands. Both placements matter:
// our block last, which is where install puts it in a file with no table,
// and our block above the participant's tables, which is where it puts it
// in every other file.
func TestReinstallingWritesTheCodexBlockWhereItWas(t *testing.T) {
	for _, tc := range []struct {
		name  string
		where codexPlacement
	}{
		{"our block is the last thing in the file", ourBlockAlone},
		{"the participant's tables sit below our block", afterOurBlock},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ops, cfgPath, before := installedCodexConfig(t, tc.where)
			stale := staleOurTable(t, before)
			m.files[codexConfigPath] = []byte(stale)
			wantPre, wantPost := outsideOurMarkers(t, stale)

			if code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
				t.Fatalf("install: exit %d\n%s%s", code, out, errOut)
			}
			got := string(m.files[codexConfigPath])
			if !strings.Contains(got, refreshedHost) {
				t.Fatalf("the install did not refresh our own table, so nothing about its position is being tested:\n%s", got)
			}
			gotPre, gotPost := outsideOurMarkers(t, got)
			if gotPre != wantPre {
				t.Errorf("bytes BEFORE our block changed\n got %q\nwant %q", gotPre, wantPre)
			}
			if gotPost != wantPost {
				t.Errorf("bytes AFTER our block changed\n got %q\nwant %q", gotPost, wantPost)
			}
			if n := strings.Count(got, agentsMarkerBegin); n != 1 {
				t.Errorf("begin markers after the install: %d, want 1:\n%s", n, got)
			}
		})
	}
}

// A file the participant edits on Windows has CRLF endings, and our block is
// written with LF. What must hold is the same thing: their bytes, on either
// side of our markers, exactly as they were.
//
// Their tables are below our block, and a root key above it, so the seam on
// both sides is exercised: an implementation that stripped and appended, or
// trimmed a line ending at a seam, goes red here.
func TestReinstallingKeepsACRLFCodexConfigsOwnBytes(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	m.files[codexConfigPath] = []byte("model = \"gpt-5\"\r\n\r\n[projects.'/home/u/work']\r\ntrust_level = \"trusted\"\r\n\r\n[windows]\r\nsandbox = \"unelevated\"\r\n")
	if code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install: exit %d\n%s%s", code, out, errOut)
	}
	installed := string(m.files[codexConfigPath])
	pre, _, post, _ := markedRegion([]byte(installed))
	if pre != "model = \"gpt-5\"\r\n\r\n" {
		t.Fatalf("the install changed the participant's bytes above our block: %q", pre)
	}
	if !strings.Contains(post, "[windows]\r\n") {
		t.Fatalf("this fixture is meant to leave our block mid-file, with CRLF tables below it: %q", post)
	}
	stale := staleOurTable(t, installed)
	m.files[codexConfigPath] = []byte(stale)
	wantPre, wantPost := outsideOurMarkers(t, stale)

	if code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("reinstall: exit %d\n%s%s", code, out, errOut)
	}
	gotPre, gotPost := outsideOurMarkers(t, string(m.files[codexConfigPath]))
	if gotPre != wantPre || gotPost != wantPost {
		t.Errorf("a CRLF config's own bytes changed around our block\n gotPre %q\nwantPre %q\n gotPost %q\nwantPost %q", gotPre, wantPre, gotPost, wantPost)
	}
	// And out again, byte for byte to what the participant had.
	if code, out, errOut := runAgents(t, ops, nil, "uninstall", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("uninstall: exit %d\n%s%s", code, out, errOut)
	}
	if got := string(m.files[codexConfigPath]); got != "model = \"gpt-5\"\r\n\r\n[projects.'/home/u/work']\r\ntrust_level = \"trusted\"\r\n\r\n[windows]\r\nsandbox = \"unelevated\"\r\n" {
		t.Errorf("uninstall did not return the CRLF file to its own bytes: %q", got)
	}
}

// The round trip is byte-identical wherever our own install put the block,
// which is now every file: before the first table header, or last in a file
// with none. Position is defined by the file, not remembered, so an
// uninstall that took the block out leaves an install nothing to guess.
func TestUninstallThenInstallRestoresTheCodexConfigByteForByte(t *testing.T) {
	for _, tc := range []struct {
		name  string
		where codexPlacement
	}{
		{"our block last, in a file with no table", ourBlockAlone},
		{"our block above the participant's tables", afterOurBlock},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ops, cfgPath, before := installedCodexConfig(t, tc.where)
			if code, out, errOut := runAgents(t, ops, nil, "uninstall", "-config", cfgPath, "-yes"); code != exitOK {
				t.Fatalf("uninstall: exit %d\n%s%s", code, out, errOut)
			}
			if got := string(m.files[codexConfigPath]); strings.Contains(got, agentsMarkerBegin) {
				t.Fatalf("the uninstall left our block, so the install below is not the round trip this claims:\n%s", got)
			}
			if code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
				t.Fatalf("install: exit %d\n%s%s", code, out, errOut)
			}
			if got := string(m.files[codexConfigPath]); got != before {
				t.Errorf("uninstall then install did not return the file to its prior bytes\n got %q\nwant %q", got, before)
			}
		})
	}
}

// A region found below a table header — an old block, or a block a person
// moved — cannot be written where it is: its default_permissions would be
// that table's key. Install moves it above the first header, and what must
// still hold is that no line of the participant's moves relative to another.
func TestInstallMovesABlockFoundBelowATableAboveIt(t *testing.T) {
	m, ops, cfgPath, before := installedCodexConfig(t, beforeOurBlock)
	wantLines := participantLines(t, before)
	stale := staleOurTable(t, before)
	m.files[codexConfigPath] = []byte(stale)
	if code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install: exit %d\n%s%s", code, out, errOut)
	}
	got := string(m.files[codexConfigPath])
	pre, _, _, _ := markedRegion([]byte(got))
	if holdsHeaderLine(pre) {
		t.Errorf("our block is still below a table header:\n%s", got)
	}
	if gotLines := participantLines(t, got); strings.Join(gotLines, "\n") != strings.Join(wantLines, "\n") {
		t.Errorf("a participant line moved, changed or was lost\n got %q\nwant %q\nfile:\n%s", gotLines, wantLines, got)
	}
	if doc, ok := decodeTOMLDoc(got); !ok || doc["default_permissions"] != codexProfileName {
		t.Errorf("default_permissions does not decode at the top level of the file:\n%s", got)
	}
}

// Codex's record of a hook approval is a run of tables at the end of the file
// (issue #19). Below our block — where install moves it when Codex wrote it
// inside — a refresh of our own table must leave it exactly where and what it
// is: the participant's approval is keyed by those bytes' meaning, and a
// moved or rewritten line is one a diff cannot vouch for. The shape is Linux
// 0.158.0's, bare [hooks.state] parent included.
func TestReinstallingWithHookTrustBelowTheBlockMovesNoLine(t *testing.T) {
	linux := codexConfigFixture(t, "config-0.158.0-linux.after-trust.toml")
	i := strings.Index(linux, "[hooks.state]\n")
	if i < 0 {
		t.Fatal("the Linux capture holds no bare [hooks.state] table")
	}
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	// The file before Codex's approvals: the participant's own tables.
	m.files[codexConfigPath] = []byte(linux[:i])
	if code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install: exit %d\n%s%s", code, out, errOut)
	}
	withTrust := strings.TrimRight(string(m.files[codexConfigPath]), "\n") + "\n\n" + linux[i:]
	stale := staleOurTable(t, withTrust)
	m.files[codexConfigPath] = []byte(stale)
	wantPre, wantPost := outsideOurMarkers(t, stale)
	if !strings.Contains(wantPost, "[hooks.state]") {
		t.Fatalf("this case is meant to have the approvals below our block, and they are not: %q", wantPost)
	}
	wantTrust := hookTrustOf(t, stale)

	if code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("reinstall: exit %d\n%s%s", code, out, errOut)
	}
	got := string(m.files[codexConfigPath])
	if !strings.Contains(got, refreshedHost) {
		t.Fatalf("the reinstall did not refresh our own table, so nothing about the approvals' position is being tested:\n%s", got)
	}
	gotPre, gotPost := outsideOurMarkers(t, got)
	if gotPre != wantPre || gotPost != wantPost {
		t.Errorf("a byte outside our block moved\n gotPre %q\nwantPre %q\n gotPost %q\nwantPost %q", gotPre, wantPre, gotPost, wantPost)
	}
	if !reflect.DeepEqual(hookTrustOf(t, got), wantTrust) {
		t.Errorf("Codex's record of the approvals changed\n got %v\nwant %v", hookTrustOf(t, got), wantTrust)
	}
}
