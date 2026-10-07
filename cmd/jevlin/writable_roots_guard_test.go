package main

// The guard behind hard invariant 19: code outside the sandbox touches a
// directory a sandboxed agent can write (the state, intake, sessions and
// spool directories; pkg/fsx/confined.go) only in ways that agent's files
// cannot redirect or stall.
//
// It is the boundary_test.go of the filesystem. A review that lists the
// sites it knows misses the next one, the way #49, #55 and #58 each missed
// sites the others or the triage found; so this walks every non-test Go
// file in the module and fails on any reference to a call that writes,
// renames, removes, creates, chmods or opens a file, unless that reference
// is on the list below with the reason it is safe. A new one fails until
// someone decides which it is: the forbidden state is not choosing
// (invariant 3's rule, applied to files). A listed one that is gone fails
// too, so the list cannot rot into a list of things that used to be true.
//
// The reasons fall in four kinds. "root" is a writable root, safe by
// construction: a name removed or renamed (neither follows a link), the
// root itself created, or a write through fsx's exclusive staging. "fsx" is
// the primitives themselves. "outside" is a file no sandbox is given:
// host configuration, the installation's own root, the wallet, the binary.
// "root and outside" is both.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// watchedFileCalls are the calls the walk reports, by package name.
var watchedFileCalls = map[string]map[string]bool{
	"os": {"WriteFile": true, "OpenFile": true, "Create": true, "CreateTemp": true, "Rename": true, "Remove": true,
		"RemoveAll": true, "Mkdir": true, "MkdirAll": true, "MkdirTemp": true, "Link": true, "Symlink": true,
		"Chmod": true, "Truncate": true, "ReadFile": true, "Open": true},
	"fsx": {"WriteFileAtomic": true, "WriteFileExclusive": true, "MoveFileDurable": true, "RemoveFileDurable": true},
}

type fileCallSite struct{ file, fn, call string }

// classifiedFileCalls is every reference the walk finds, with why it is safe.
var classifiedFileCalls = map[fileCallSite]string{
	{"cmd/jevlin/agents.go", "realAgentOps", "os.MkdirAll"}:                             "outside: host config directories (~/.claude, ~/.codex, ~/.cursor, ...)",
	{"cmd/jevlin/agents.go", "realAgentOps", "os.ReadFile"}:                             "outside: host config files",
	{"cmd/jevlin/agents.go", "realAgentOps", "os.RemoveAll"}:                            "outside: host skill directories",
	{"cmd/jevlin/agents.go", "realAgentOps", "os.WriteFile"}:                            "outside: host config files",
	{"cmd/jevlin/connect.go", "writeResumeStamp", "fsx.WriteFileAtomic"}:                "root: the state dir's resume stamp; fsx stages under an exclusive random name",
	{"cmd/jevlin/credentials.go", "cmdLogin", "os.Remove"}:                              "outside: credentials.json, in the installation's own root",
	{"cmd/jevlin/credentials.go", "readCredentials", "os.ReadFile"}:                     "outside: credentials.json, in the installation's own root",
	{"cmd/jevlin/credentials.go", "writeCredentials", "os.MkdirAll"}:                    "outside: the installation's own root",
	{"cmd/jevlin/credentials.go", "writeCredentials", "os.OpenFile"}:                    "outside: credentials.json, in the installation's own root",
	{"cmd/jevlin/credentials.go", "writeCredentials", "os.Remove"}:                      "outside: credentials.json, in the installation's own root",
	{"cmd/jevlin/credentials.go", "writeCredentials", "os.Rename"}:                      "outside: credentials.json, in the installation's own root",
	{"cmd/jevlin/doctor.go", "realIntakeProbeOps", "fsx.WriteFileAtomic"}:               "root: doctor's inert probe in the intake dir, staged under an exclusive random name",
	{"cmd/jevlin/doctor.go", "realIntakeProbeOps", "os.MkdirAll"}:                       "root: creates the intake dir itself, which a sandboxed command cannot replace",
	{"cmd/jevlin/doctor.go", "realIntakeProbeOps", "os.Remove"}:                         "root: removes a name, never what it points at",
	{"cmd/jevlin/flush.go", "promoteIntake", "fsx.WriteFileAtomic"}:                     "root: a spool record at the spool's top level, staged under an exclusive random name",
	{"cmd/jevlin/flush.go", "promoteIntake", "os.Remove"}:                               "root: removes an intake record's name",
	{"cmd/jevlin/flush.go", "runFlushAdmitted", "os.MkdirAll"}:                          "outside: the installation's own root, for flush.lock",
	{"cmd/jevlin/flushlock.go", "ensureFlushLockFile", "os.OpenFile"}:                   "outside: flush.lock, in the installation's own root",
	{"cmd/jevlin/hook.go", "readFileTail", "os.Open"}:                                   "outside: the host's own transcript, named by its payload",
	{"cmd/jevlin/hook.go", "realHookOps", "os.MkdirAll"}:                                "root: creates the sessions or state dir itself",
	{"cmd/jevlin/hook.go", "realHookOps", "os.Remove"}:                                  "root: removes a name, never what it points at",
	{"cmd/jevlin/hook.go", "realHookOps", "os.Rename"}:                                  "root: renames our exclusively created temporary file onto a top-level name; a rename replaces the name, never what it pointed at",
	{"cmd/jevlin/lifecycle.go", "lifecycleExclusion.release", "os.Remove"}:              "root and outside: removes lock files this operation created, by name",
	{"cmd/jevlin/miner.go", "recordSearchEpoch", "fsx.WriteFileAtomic"}:                 "root: the intake dir's recorded epoch, staged under an exclusive random name",
	{"cmd/jevlin/miner.go", "writeFlushStamp", "os.MkdirAll"}:                           "root: creates the state dir itself",
	{"cmd/jevlin/miner.go", "writeIntake", "fsx.WriteFileAtomic"}:                       "root: an intake record at the intake dir's top level, staged under an exclusive random name",
	{"cmd/jevlin/miner.go", "writeIntake", "os.MkdirAll"}:                               "root: creates the intake dir itself",
	{"cmd/jevlin/minerlock_unix.go", "tryFlushLock", "os.Open"}:                         "outside: flush.lock, in the installation's own root",
	{"cmd/jevlin/minerlock_unix.go", "tryFlushLock", "os.OpenFile"}:                     "outside: flush.lock, in the installation's own root",
	{"cmd/jevlin/ownership.go", "npmLayoutKind", "os.ReadFile"}:                         "outside: an npm package's own package.json",
	{"cmd/jevlin/setup.go", "setupRun.admit", "os.MkdirAll"}:                            "outside: the installation directory",
	{"cmd/jevlin/setup.go", "setupRun.directories", "os.MkdirAll"}:                      "outside: setup creates the installation's directories, before any agent runs in them",
	{"cmd/jevlin/setup_adopt.go", "adoption.doMove", "fsx.MoveFileDurable"}:             "outside: setup's adoption, from a set-aside sibling, ownership-checked, at a terminal",
	{"cmd/jevlin/setup_adopt.go", "adoption.finish", "os.Remove"}:                       "outside: setup's adoption",
	{"cmd/jevlin/setup_adopt.go", "adoption.mergeDir", "os.Mkdir"}:                      "outside: setup's adoption",
	{"cmd/jevlin/setup_adopt.go", "adoption.mergeDir", "os.Remove"}:                     "outside: setup's adoption",
	{"cmd/jevlin/setup_adopt.go", "bundleTxn.makeRoom", "os.Mkdir"}:                     "outside: setup's adoption",
	{"cmd/jevlin/setup_adopt.go", "bundleTxn.makeRoom", "os.Remove"}:                    "outside: setup's adoption",
	{"cmd/jevlin/setup_config.go", "publishSetupConfig", "fsx.WriteFileAtomic"}:         "outside: jevlin.toml",
	{"cmd/jevlin/setup_config.go", "readExistingConfig", "os.ReadFile"}:                 "outside: jevlin.toml",
	{"cmd/jevlin/setup_config.go", "validateConfigFile", "os.CreateTemp"}:               "outside: a temporary copy for validation",
	{"cmd/jevlin/setup_config.go", "validateConfigFile", "os.Remove"}:                   "outside: its own temporary copy",
	{"cmd/jevlin/setup_env.go", "applyUserEnvironment", "fsx.WriteFileAtomic"}:          "outside: the shell profile or the environment journal",
	{"cmd/jevlin/setup_env.go", "applyUserEnvironmentRevert", "os.Remove"}:              "outside: the environment journal",
	{"cmd/jevlin/setup_env.go", "profileTarget", "os.ReadFile"}:                         "outside: the shell profile",
	{"cmd/jevlin/setup_env.go", "readEnvJournal", "os.ReadFile"}:                        "outside: the environment journal",
	{"cmd/jevlin/setup_env_unix.go", "restrictToOwner", "os.Chmod"}:                     "outside: setup's own directories",
	{"cmd/jevlin/setup_env_unix.go", "setupRun.environmentStep", "fsx.WriteFileAtomic"}: "outside: the shell profile",
	{"cmd/jevlin/turn_end.go", "cmdTurnEnd", "os.Remove"}:                               "root: removes the queued turn end's name",
	{"cmd/jevlin/turn_end.go", "sweepTurnMarks", "os.Remove"}:                           "root: removes stale turn marks by name",
	{"cmd/jevlin/uninstall.go", "uninstallRun.applyBinary", "os.Remove"}:                "outside: the binary",
	{"cmd/jevlin/uninstall.go", "uninstallRun.applyBinary", "os.RemoveAll"}:             "outside: the binary's directory",
	{"cmd/jevlin/uninstall.go", "uninstallRun.applyEnvironment", "fsx.WriteFileAtomic"}: "outside: the shell profile",
	{"cmd/jevlin/uninstall.go", "uninstallRun.applyPurge", "os.RemoveAll"}:              "root: purge removes the directories; RemoveAll removes a link, never what it points at",
	{"cmd/jevlin/uninstall.go", "uninstallRun.removeHomeIfEmpty", "os.Remove"}:          "outside: the installation directory",
	{"cmd/jevlin/wallet_journal.go", "removePendingTx", "os.Remove"}:                    "outside: the wallet, which no sandbox may write",
	{"cmd/jevlin/wallet_store.go", "<package level>", "fsx.WriteFileAtomic"}:            "outside: the wallet",
	{"cmd/jevlin/wallet_store.go", "openWalletDir", "os.MkdirAll"}:                      "outside: the wallet",
	{"cmd/jevlin/wallet_store.go", "readWalletFile", "os.ReadFile"}:                     "outside: the wallet",
	{"internal/selfupdate/candidate.go", "StageCandidate", "os.CreateTemp"}:             "outside: the binary's directory",
	{"internal/selfupdate/candidate.go", "StageCandidate", "os.Remove"}:                 "outside: the binary's directory",
	{"internal/selfupdate/prepare.go", "Prepared.Discard", "os.Remove"}:                 "outside: the binary's directory",
	{"internal/selfupdate/prepare.go", "Prepared.DiscardAfterInstall", "os.Remove"}:     "outside: the binary's directory",
	{"internal/selfupdate/rename_unix.go", "platformRenameNew", "os.Rename"}:            "outside: the binary's directory",
	{"internal/selfupdate/rename_unix.go", "platformRenameReplace", "os.Rename"}:        "outside: the binary's directory",
	{"internal/selfupdate/replace.go", "durableSnapshot", "os.CreateTemp"}:              "outside: the binary's directory",
	{"internal/selfupdate/replace.go", "durableSnapshot", "os.Open"}:                    "outside: the binary",
	{"internal/selfupdate/replace.go", "durableSnapshot", "os.Remove"}:                  "outside: the binary's directory",
	{"internal/selfupdate/replace.go", "replaceOpsWithin", "os.Remove"}:                 "outside: the binary's directory",
	{"internal/selfupdate/replace.go", "reservePath", "os.CreateTemp"}:                  "outside: the binary's directory",
	{"internal/selfupdate/replace.go", "reservePath", "os.Remove"}:                      "outside: the binary's directory",
	{"internal/selfupdate/rollback.go", "Updater.Rollback", "os.Remove"}:                "outside: the binary's directory",
	{"internal/selfupdate/rollback.go", "readPrevious", "os.Open"}:                      "outside: the previous binary",
	{"pkg/auth/health.go", "removeStateFile", "os.Remove"}:                              "root: removes a health record's name",
	{"pkg/auth/store.go", "OpenStore", "os.MkdirAll"}:                                   "root: creates the state dir itself",
	{"pkg/auth/store.go", "Store.ClearPayoutBindingHeld", "os.Remove"}:                  "root: removes a name",
	{"pkg/auth/store.go", "Store.ClearPendingRegistration", "os.Remove"}:                "root: removes a name",
	{"pkg/auth/store.go", "Store.ClearRevokePending", "os.Remove"}:                      "root: removes a name",
	{"pkg/auth/store.go", "Store.DeleteRefreshToken", "os.Remove"}:                      "root: removes a name",
	{"pkg/auth/store.go", "Store.PreserveCorruptAgentRegistration", "os.Rename"}:        "root: renames agent.json aside; a rename replaces the name, never what it pointed at",
	{"pkg/auth/store.go", "Store.SaveRefreshToken", "fsx.WriteFileAtomic"}:              "root: staged under an exclusive random name",
	{"pkg/auth/store.go", "Store.createExclusive", "fsx.WriteFileExclusive"}:            "root: staged under an exclusive random name, published without replacing",
	{"pkg/auth/store.go", "Store.saveStateFile", "fsx.WriteFileAtomic"}:                 "root: staged under an exclusive random name",
	{"pkg/config/config.go", "rawConfig.applyFile", "os.ReadFile"}:                      "outside: the config file",
	{"pkg/config/config.go", "rawConfig.finish", "os.ReadFile"}:                         "outside: the operator's upstream CA file",
	{"pkg/fsx/atomic.go", "defaultOperations", "os.CreateTemp"}:                         "fsx: the exclusive random staging name",
	{"pkg/fsx/atomic.go", "defaultOperations", "os.Remove"}:                             "fsx: removes its own staging name",
	{"pkg/fsx/confined.go", "CreateNew", "os.OpenFile"}:                                 "fsx: O_EXCL, never an existing name",
	{"pkg/fsx/confined.go", "CreateNew", "os.Remove"}:                                   "fsx: removes the file it just created",
	{"pkg/fsx/confined_unix.go", "openLock", "os.OpenFile"}:                             "fsx: O_NOFOLLOW, then a regular-file check",
	{"pkg/fsx/confined_unix.go", "openNoWait", "os.OpenFile"}:                           "fsx: O_NONBLOCK, then a regular-file check",
	{"pkg/fsx/confined_windows.go", "openNoWait", "os.Open"}:                            "fsx: then a regular-file check; no FIFO lives at a Windows name",
	{"pkg/fsx/publication_unix.go", "movePublication", "os.Rename"}:                     "fsx: the durable move its callers confine",
	{"pkg/fsx/publication_unix.go", "publish", "os.Link"}:                               "fsx: publishes its own staged file without replacing",
	{"pkg/fsx/publication_unix.go", "publish", "os.Rename"}:                             "fsx: publishes its own staged file",
	{"pkg/fsx/publication_unix.go", "removeDurable", "os.Remove"}:                       "fsx: removes a name",
	{"pkg/fsx/publication_unix.go", "syncDirectory", "os.Open"}:                         "fsx: opens a directory to fsync it",
	{"pkg/fsx/publication_windows.go", "removeDurable", "os.CreateTemp"}:                "fsx: the exclusive random staging name",
	{"pkg/fsx/publication_windows.go", "removeDurable", "os.Remove"}:                    "fsx: removes its own staging name",
	{"pkg/mining/spool/spool.go", "Open", "fsx.RemoveFileDurable"}:                      "root: removes a spool record's name at the top level",
	{"pkg/mining/spool/spool.go", "Open", "fsx.WriteFileAtomic"}:                        "root: a spool record at the top level, staged under an exclusive random name",
	{"pkg/mining/spool/spool.go", "Open", "os.MkdirAll"}:                                "root: creates the spool dir itself; the quarantine below it goes through fsx.Root",
	{"pkg/mining/spool/spool.go", "Spool.persist", "fsx.WriteFileExclusive"}:            "root: a spool record at the top level, published without replacing",
}

func TestEveryFileOperationIsClassifiedForTheWritableRoots(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	found := map[fileCallSite]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "dist", "testdata", "node_modules", "tools":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, decl := range file.Decls {
			fn := enclosingName(decl)
			ast.Inspect(decl, func(n ast.Node) bool {
				se, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if x, ok := se.X.(*ast.Ident); ok && watchedFileCalls[x.Name][se.Sel.Name] {
					found[fileCallSite{rel, fn, x.Name + "." + se.Sel.Name}] = true
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var unlisted, stale []string
	for site := range found {
		if _, ok := classifiedFileCalls[site]; !ok {
			unlisted = append(unlisted, fmt.Sprintf("%s %s %s", site.file, site.fn, site.call))
		}
	}
	for site := range classifiedFileCalls {
		if !found[site] {
			stale = append(stale, fmt.Sprintf("%s %s %s", site.file, site.fn, site.call))
		}
	}
	sort.Strings(unlisted)
	sort.Strings(stale)
	for _, s := range unlisted {
		t.Errorf("unclassified file operation: %s\n\tIf it can touch the state, intake, sessions or spool directory, use pkg/fsx's confined operations"+
			" (CreateNew, ReadRegular, OpenLock, Root); then add it to classifiedFileCalls with the reason it is safe.", s)
	}
	for _, s := range stale {
		t.Errorf("classifiedFileCalls lists a file operation that is no longer there: %s; remove the entry", s)
	}
	if len(found) == 0 {
		t.Fatal("the walk found no file operation at all; it is not looking where the code is")
	}
}

// enclosingName is a declaration's function name, with its receiver type.
func enclosingName(decl ast.Decl) string {
	fd, ok := decl.(*ast.FuncDecl)
	if !ok {
		return "<package level>"
	}
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}
