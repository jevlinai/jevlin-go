package main

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jevlinai/jevlin-go/internal/termtext"
	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// The structural guard for what the state directory's records can put on
// the participant's terminal. The state directory is a writable root of
// Codex's sandbox (hard invariant 19), and status, doctor, connect and mining
// enable print from its records. Rather than list the fields someone thought
// of, this builds an installation through the real commands, takes every
// file they left in the state directory, and plants a terminal escape in
// every string a record holds, one at a time, then runs every command that
// prints and fails if a control byte comes back out.
//
// The forbidden state is not choosing: a file the run leaves that is not in
// stateRecordFiles fails until someone says whether its strings are printed.
// A record whose Go type is named there is planted in every string field of
// that type too, so a field the run happened to leave empty is covered.
//
// Each string is planted once per character class (plantedClasses), not
// with one payload holding them all: a load check narrowed to refuse only
// ESC still refused the combined payload, so the guard passed while a
// right-to-left override or a tag character went through. The spool, the
// one subdirectory, is planted too: its records are not printed by status,
// but a duplicate client_record_id reached a foreground flush's stderr.
//
// What it cannot catch: a record the fixture run never writes (add the step
// that writes it to stateRecordFixture), a record printed only by a command
// not in printingCommands, and text outside the state directory.

// stateRecordFiles classifies every file the fixture leaves in the state
// directory. A JSON record is planted; anything else names why it is not.
var stateRecordFiles = map[string]stateRecordClass{
	"agent.json":               {json: true, typ: reflect.TypeOf(auth.AgentRegistration{})},
	"payout_declared.json":     {json: true},
	"payout_binding_held.json": {json: true, typ: reflect.TypeOf(auth.PayoutBindingHeld{})},
	"health_decision.json":     {json: true, typ: reflect.TypeOf(auth.HealthRecord{})},
	"health_capture.json":      {json: true, typ: reflect.TypeOf(auth.HealthRecord{})},
	"health_flush.json":        {json: true, typ: reflect.TypeOf(auth.HealthRecord{})},
	"mining_decision.json":     {json: true},
	"epoch_conflicts.json":     {json: true},
	"revoke_pending.json":      {json: true},
	"connect_resume.json":      {json: true},
	"flush.json":               {json: true},
	"dpop.key":                 {why: "a PEM key, read by the AS client and never printed"},
	"refresh.token":            {why: "an opaque token, sent to the AS and never printed"},
	"participation.secret":     {why: "32 random bytes, never printed"},
	"connect.lock":             {why: "a lock file; its content is never read"},
	"refresh.token.lock":       {why: "a lock file; its content is never read"},
}

type stateRecordClass struct {
	json bool
	typ  reflect.Type // the production type, when one record type owns the file
	why  string
}

// stateRecordFixture builds an installation through the real commands and
// writers: registered, claimed with mining, enrolled, its payout address
// declared, with a held binding, every health component marked, an epoch
// conflict, a revoke pending and a flush stamp.
func stateRecordFixture(t *testing.T) (cfgPath, stateDir string) {
	t.Helper()
	_, _, cfgPath, stateDir = enrolledButUndeclared(t)
	if err := testPayoutRecord(t, stateDir).Save(participantAddress); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := runConnect(t, cfgPath, nil, "-resume"); code != exitOK {
		t.Fatalf("declaring resume failed: %d\n%s\n%s", code, out, errOut)
	}
	store := mustStore(t, stateDir)
	if _, ok, err := store.LoadPayoutDeclared(); err != nil || !ok {
		t.Fatalf("the fixture's address was not declared: ok=%v err=%v", ok, err)
	}
	for _, mark := range []struct {
		c auth.HealthComponent
		r auth.HealthReason
	}{
		{auth.HealthDecision, auth.HealthDecisionUnreadable},
		{auth.HealthCapture, auth.HealthSandboxRestricted},
		{auth.HealthFlush, auth.HealthSubmissionFailed},
	} {
		if err := store.MarkHealth(mark.c, mark.r, "the detail of "+string(mark.c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SavePayoutBindingHeld(participantAddress, plantedAddress, auth.HeldAddressInUse); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveEpochConflict(7, 1042); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRevokePending(); err != nil {
		t.Fatal(err)
	}
	// [miner] on, so a foreground flush runs far enough to read the spool.
	raw, err := os.ReadFile(cfgPath) // #nosec G304 -- the test's own config
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "[miner]") {
		writeFileT(t, cfgPath, string(raw)+"\n[miner]\nenabled = true\n")
	}
	cfg := mustLoadConfig(t, cfgPath)
	if err := saveFlushStamp(store, flushStampPath(cfg.Mining), flushStamp{V: 1, SlotID: 7, TargetEpoch: 1042, LastAS: time.Now(), LastFlush: time.Now()}, io.Discard); err != nil {
		t.Fatal(err)
	}
	return cfgPath, stateDir
}

// printingCommands is every command that prints from the state directory's
// records, in both of its modes.
func printingCommands(t *testing.T, cfgPath string) map[string]string {
	t.Helper()
	out := map[string]string{}
	run := func(name string, f func(stdout, stderr io.Writer)) {
		var o, e bytes.Buffer
		f(&o, &e)
		out[name] = o.String() + e.String()
	}
	run("status", func(o, e io.Writer) { _ = statusMain([]string{"-config", cfgPath}, o, e, noEnv) })
	run("status -json", func(o, e io.Writer) { _ = statusMain([]string{"-config", cfgPath, "-json"}, o, e, noEnv) })
	run("doctor", func(o, e io.Writer) { _ = cmdDoctor([]string{"-config", cfgPath}, o, e) })
	run("doctor -json", func(o, e io.Writer) { _ = cmdDoctor([]string{"-config", cfgPath, "-json"}, o, e) })
	run("connect -json", func(o, _ io.Writer) { emitMachine(o, connectEnvelope(cfgPath, noEnv, exitOK, "")) })
	_, mo, me := runMiningEnable(t, cfgPath)
	out["mining enable"] = mo + me
	_, co, ce := runConnect(t, cfgPath, nil)
	out["connect"] = co + ce
	run("flush", func(o, e io.Writer) { _ = cmdFlush([]string{"-config", cfgPath, "-force"}, o, e, noEnv) })
	return out
}

// plantedClasses is one planted value per class of character a terminal
// acts on or a reader cannot see. A raw invalid byte is not among them: every
// record here is JSON, and encoding/json decodes an invalid byte as U+FFFD
// before anything can print it (terminal_writer_test.go covers the raw byte
// for what flush prints).
var plantedClasses = map[string]string{
	"OSC 52 (ESC, BEL)": "x\x1b]52;c;ZXZpbA==\ay",
	"C1 CSI":            "x\u009b2Jy",
	"right-to-left":     "x\u202ey",
	"tag characters":    "x\U000e0049\U000e0047y",
	"zero-width space":  "x\u200by",
	"line separator":    "x\u2028y",
}

// requireNothingPlanted fails if any refused character of planted came out.
// In JSON, ESC and BEL come out escaped as text, which is not an escape;
// every other class would come out raw.
func requireNothingPlanted(t *testing.T, what, out, planted string) {
	t.Helper()
	for _, r := range planted {
		if termtext.HasControlChar(string(r)) && strings.ContainsRune(out, r) {
			t.Fatalf("%s carried U+%04X to the terminal:\n%q", what, r, out)
		}
	}
}

// jsonLeaf is the path to one string in a decoded record: object keys and
// array indexes.
type jsonLeaf []any

func (l jsonLeaf) String() string {
	parts := make([]string, len(l))
	for i, p := range l {
		b, _ := json.Marshal(p)
		parts[i] = string(b)
	}
	return strings.Join(parts, ".")
}

func stringLeaves(v any, at jsonLeaf, into *[]jsonLeaf) {
	switch x := v.(type) {
	case string:
		*into = append(*into, append(jsonLeaf(nil), at...))
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			stringLeaves(x[k], append(at, k), into)
		}
	case []any:
		for i := range x {
			stringLeaves(x[i], append(at, i), into)
		}
	}
}

func plantAt(v any, at jsonLeaf, value string) any {
	if len(at) == 0 {
		return value
	}
	switch x := v.(type) {
	case map[string]any:
		k := at[0].(string)
		x[k] = plantAt(x[k], at[1:], value)
		return x
	case []any:
		i := at[0].(int)
		x[i] = plantAt(x[i], at[1:], value)
		return x
	}
	return value
}

func snapshotStateDir(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	snap := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		b, err := os.ReadFile(path) // #nosec G304 G122 -- the test's own state dir, which nothing else writes
		snap[rel] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func restoreStateDir(t *testing.T, dir string, snap map[string][]byte) {
	t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	for rel, b := range snap {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNoStateRecordCarriesATerminalEscapeToTheOutput(t *testing.T) {
	cfgPath, stateDir := stateRecordFixture(t)
	snap := snapshotStateDir(t, stateDir)

	var files []string
	for rel := range snap {
		files = append(files, rel)
	}
	sort.Strings(files)
	planted := 0
	for _, rel := range files {
		if strings.Contains(rel, string(filepath.Separator)) {
			continue // the spool and its quarantine: evidence records, counted and never printed
		}
		class, ok := stateRecordFiles[rel]
		if !ok {
			t.Errorf("the state directory holds %s, which nobody has classified: add it to stateRecordFiles, "+
				"planted if any command prints a string from it, with the reason if none does", rel)
			continue
		}
		if !class.json {
			continue
		}
		var doc any
		if err := json.Unmarshal(snap[rel], &doc); err != nil {
			t.Fatalf("%s does not decode as the JSON its class says: %v", rel, err)
		}
		var leaves []jsonLeaf
		stringLeaves(doc, nil, &leaves)
		if obj, isObj := doc.(map[string]any); isObj && class.typ != nil {
			for i := 0; i < class.typ.NumField(); i++ {
				f := class.typ.Field(i)
				name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
				kind := f.Type.Kind()
				isText := kind == reflect.String || (kind == reflect.Slice && f.Type.Elem().Kind() == reflect.String)
				if !f.IsExported() || name == "" || name == "-" || !isText {
					continue
				}
				if _, present := obj[name]; !present {
					leaves = append(leaves, jsonLeaf{name}) // left empty by the run, planted anyway
				}
			}
		}
		for _, leaf := range leaves {
			t.Run(rel+"/"+leaf.String(), func(t *testing.T) {
				for class, planted := range plantedClasses {
					restoreStateDir(t, stateDir, snap)
					var fresh any
					if err := json.Unmarshal(snap[rel], &fresh); err != nil {
						t.Fatal(err)
					}
					raw, err := json.Marshal(plantAt(fresh, leaf, planted))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(stateDir, rel), raw, 0o600); err != nil {
						t.Fatal(err)
					}
					for name, out := range printingCommands(t, cfgPath) {
						requireNothingPlanted(t, name+" with "+rel+" "+leaf.String()+" planted with "+class, out, planted)
					}
				}
			})
			planted++
		}
	}
	if planted < 20 {
		t.Fatalf("only %d strings were planted; the fixture no longer builds the records this guard is for", planted)
	}

	// The spool: two records with one client_record_id, the duplicate a
	// flush refuses to deliver and names in its error.
	spoolDir := mustLoadConfig(t, cfgPath).Mining.SpoolDir
	for class, planted := range plantedClasses {
		t.Run("spool/client_record_id/"+class, func(t *testing.T) {
			restoreStateDir(t, stateDir, snap)
			for _, name := range []string{"a.json", "b.json"} {
				raw, err := json.Marshal(map[string]any{
					"client_record_id": planted, "slot_id": 7, "target_epoch": 1042,
					"observation": map[string]any{}, "spooled_at": time.Now().UTC(), "attempts": 0,
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(spoolDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(spoolDir, name), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			outs := printingCommands(t, cfgPath)
			if !strings.Contains(outs["flush"], "multiple active locations") {
				t.Fatalf("the planted duplicate did not reach flush's error, so this proves nothing:\n%s", outs["flush"])
			}
			for name, out := range outs {
				requireNothingPlanted(t, name+" with a spool record's client_record_id planted with "+class, out, planted)
			}
		})
	}
	restoreStateDir(t, stateDir, snap)
}
