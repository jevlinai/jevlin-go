package main

// The guard behind hard invariant 19: code outside the sandbox touches a
// directory a sandboxed agent can write (the state and sessions
// directories; pkg/fsx/confined.go) only in ways that agent's files cannot
// redirect or stall.
//
// It is the boundary_test.go of the filesystem. A review that lists the
// sites it knows misses the next one, the way #49, #55 and #58 each missed
// sites the others or the triage found; so this walks every non-test Go
// file in the module and fails on any reference to a function, in a fixed
// set of packages, that opens, creates, writes, renames, removes, links or
// chmods a file or lists a directory, unless that (file, function, call)
// is listed below with the number of times it occurs and the reason it is
// safe. A new one fails until someone decides which it is: the forbidden
// state is not choosing (invariant 3's rule, applied to files). A listed
// one that is gone, or occurs a different number of times, fails too, so
// the list stays the code's.
//
// What it sees: references by any import name (aliases resolved through
// the file's imports; a dot import of a watched package is itself a
// failure), in os, io/ioutil, syscall, golang.org/x/sys/unix,
// golang.org/x/sys/windows and this module's pkg/fsx. os.OpenRoot is listed
// only inside pkg/fsx, so an *os.Root anywhere else is a failure. What it
// does not see: methods on a handle a listed open returned (the open's
// entry answers for its handle), and a call through a function value that
// came from another package. Those are a review's to catch.
//
// The reasons fall in four kinds. "root" is a writable root, safe by
// construction: a name removed or renamed (neither follows a link), the
// root itself created or listed, a write through fsx's exclusive staging,
// or one of fsx's confined operations. "fsx" is the primitives themselves.
// "outside" is a file no sandbox is given: host configuration, the
// installation's own directory, the binary. "root and outside" is both.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// watchedFileCalls are the functions the walk reports, by import path.
var watchedFileCalls = map[string]map[string]bool{
	"os": nameSet("WriteFile", "OpenFile", "Create", "CreateTemp", "Rename", "Remove", "RemoveAll", "Mkdir", "MkdirAll",
		"MkdirTemp", "Link", "Symlink", "Chmod", "Chown", "Lchown", "Chtimes", "Truncate", "ReadFile", "Open", "ReadDir",
		"OpenRoot", "OpenInRoot", "CopyFS"),
	"io/ioutil": nameSet("WriteFile", "ReadFile", "ReadDir", "TempFile", "TempDir"),
	"syscall": nameSet("Open", "Openat", "Creat", "CreateFile", "Rename", "Renameat", "Unlink", "Unlinkat", "Rmdir", "Mkdir",
		"Mkdirat", "Mkfifo", "Link", "Symlink", "Chmod", "Fchmodat", "Truncate", "MoveFile", "DeleteFile", "CreateDirectory",
		"RemoveDirectory"),
	"golang.org/x/sys/unix": nameSet("Open", "Openat", "Openat2", "Creat", "Rename", "Renameat", "Renameat2", "Unlink",
		"Unlinkat", "Rmdir", "Mkdir", "Mkdirat", "Mkfifo", "Mkfifoat", "Link", "Linkat", "Symlink", "Symlinkat", "Chmod",
		"Fchmodat", "Truncate"),
	"golang.org/x/sys/windows": nameSet("CreateFile", "MoveFileEx", "MoveFile", "DeleteFile", "CreateDirectory",
		"RemoveDirectory", "CreateHardLink", "CreateSymbolicLink", "SetFileInformationByHandle", "NtCreateFile",
		"NtSetInformationFile"),
	"github.com/jevlinai/jevlin-go/pkg/fsx": nameSet("WriteFileAtomic", "WriteFileExclusive", "ReplaceFile", "MoveFileDurable",
		"RemoveFileDurable", "CreateNew", "ReadRegular", "ReadRegularNoFollow", "OpenLock", "OpenLockExisting", "OpenRoot",
		"SyncDirectory"),
}

func nameSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

type fileCallSite struct{ file, fn, call string }

type fileCallClass struct {
	n   int
	why string
}

// classifiedFileCalls is every reference the walk finds, how many times,
// and why it is safe.
var classifiedFileCalls = map[fileCallSite]fileCallClass{
	{"cmd/jevlin/agents.go", "realAgentOps", "os.MkdirAll"}:                             {1, "outside: host config directories (~/.claude, ~/.codex, ~/.cursor, ...)"},
	{"cmd/jevlin/agents.go", "realAgentOps", "os.ReadFile"}:                             {1, "outside: host config files"},
	{"cmd/jevlin/agents.go", "realAgentOps", "os.RemoveAll"}:                            {1, "outside: host skill directories"},
	{"cmd/jevlin/agents.go", "realAgentOps", "os.Remove"}:                               {1, "outside: one name of ours beside the config (search-tier), lstat-checked not to be a directory, so a link is removed as the link; never recursive"},
	{"cmd/jevlin/agents.go", "realAgentOps", "fsx.WriteFileAtomic"}:                     {1, "outside: search-tier beside the config, in the installation's own root; an exclusively created temporary renamed over the name, so a link there is replaced, never written through"},
	{"cmd/jevlin/agents.go", "realAgentOps", "fsx.ReplaceFile"}:                         {1, "outside: host config files, replaced whole through a temporary file beside the file a link names (issue #37)"},
	{"cmd/jevlin/connect.go", "readResumeStamp", "fsx.ReadRegular"}:                     {1, "root: the state dir's resume stamp, read without waiting and bounded"},
	{"cmd/jevlin/connect.go", "writeResumeStamp", "fsx.WriteFileAtomic"}:                {1, "root: the state dir's resume stamp; fsx stages under an exclusive random name"},
	{"cmd/jevlin/credentials.go", "cmdLogin", "os.Remove"}:                              {1, "outside: credentials.json, in the installation's own root"},
	{"cmd/jevlin/search.go", "readTierSetting", "fsx.ReadRegularNoFollow"}:              {1, "outside: search-tier beside the config, in the installation's own root, as credentials.json; one no-follow open, bounded, and a word this client cannot name is not forwarded"},
	{"cmd/jevlin/credentials.go", "readCredentials", "fsx.ReadRegularNoFollow"}:         {1, "outside, unless a layout nests the installation in a root (agents install refuses it): one no-follow open, mode checked on it"},
	{"cmd/jevlin/credentials.go", "writeCredentials", "os.MkdirAll"}:                    {1, "outside: the installation's own root"},
	{"cmd/jevlin/credentials.go", "writeCredentials", "os.OpenFile"}:                    {1, "outside: credentials.json, in the installation's own root"},
	{"cmd/jevlin/credentials.go", "writeCredentials", "os.Remove"}:                      {1, "outside: credentials.json, in the installation's own root"},
	{"cmd/jevlin/credentials.go", "writeCredentials", "os.Rename"}:                      {1, "outside: credentials.json, in the installation's own root"},
	{"cmd/jevlin/hook.go", "readFileTail", "os.Open"}:                                   {1, "outside: the host's own transcript, named by its payload"},
	{"cmd/jevlin/hook.go", "realHookOps", "fsx.CreateNew"}:                              {1, "root: the hook's writes, O_EXCL, never an existing name"},
	{"cmd/jevlin/hook.go", "realHookOps", "fsx.ReadRegular"}:                            {1, "root: the hook's reads, without waiting and bounded"},
	{"cmd/jevlin/hook.go", "realHookOps", "os.MkdirAll"}:                                {1, "root: creates the sessions or state dir itself"},
	{"cmd/jevlin/hook.go", "realHookOps", "os.Remove"}:                                  {1, "root: removes a name, never what it points at"},
	{"cmd/jevlin/hook.go", "realHookOps", "os.Rename"}:                                  {1, "root: renames our exclusively created temporary file onto a top-level name; a rename replaces the name, never what it pointed at"},
	{"cmd/jevlin/lifecycle.go", "lifecycleExclusion.release", "os.Remove"}:              {1, "root and outside: removes lock files this operation created, by name"},
	{"cmd/jevlin/lineage.go", "listTempFiles", "os.ReadDir"}:                            {1, "root: lists a root's top level for the temporary-file sweep; O_DIRECTORY refuses a FIFO"},
	{"cmd/jevlin/filelock_unix.go", "tryLockFile", "fsx.OpenLock"}:                      {1, "root and outside: connect.lock in the state dir, and the lifecycle locks; no link followed"},
	{"cmd/jevlin/filelock_windows.go", "tryLockFile", "fsx.OpenLock"}:                   {1, "root and outside: connect.lock and the lifecycle locks; no reparse point followed, share mode 0"},
	{"cmd/jevlin/ownership.go", "npmLayoutKind", "os.ReadFile"}:                         {1, "outside: an npm package's own package.json"},
	{"cmd/jevlin/setup.go", "setupRun.config", "os.Rename"}:                             {1, "outside: jevlin.toml, set aside under a name beside it in the installation's own root"},
	{"cmd/jevlin/setup.go", "setupRun.admit", "os.MkdirAll"}:                            {1, "outside: the installation directory"},
	{"cmd/jevlin/setup.go", "setupRun.directories", "os.MkdirAll"}:                      {1, "root: setup creates the installation home and the roots themselves, never a path below one"},
	{"cmd/jevlin/setup_adopt.go", "adoption.doMove", "fsx.MoveFileDurable"}:             {1, "outside: setup's adoption, from a set-aside sibling, ownership-checked, at a terminal"},
	{"cmd/jevlin/setup_adopt.go", "adoption.finish", "os.ReadDir"}:                      {1, "outside: setup's adoption, at a terminal"},
	{"cmd/jevlin/setup_adopt.go", "adoption.finish", "os.Remove"}:                       {1, "outside: setup's adoption"},
	{"cmd/jevlin/setup_adopt.go", "adoption.identity", "os.ReadDir"}:                    {1, "outside: setup's adoption"},
	{"cmd/jevlin/setup_adopt.go", "adoption.mergeDir", "os.Mkdir"}:                      {1, "outside: setup's adoption"},
	{"cmd/jevlin/setup_adopt.go", "adoption.mergeDir", "os.ReadDir"}:                    {1, "outside: setup's adoption"},
	{"cmd/jevlin/setup_adopt.go", "adoption.mergeDir", "os.Remove"}:                     {1, "outside: setup's adoption"},
	{"cmd/jevlin/setup_adopt.go", "bundleTxn.makeRoom", "os.Mkdir"}:                     {1, "outside: setup's adoption"},
	{"cmd/jevlin/setup_adopt.go", "bundleTxn.makeRoom", "os.Remove"}:                    {1, "outside: setup's adoption"},
	{"cmd/jevlin/setup_adopt.go", "dirEmpty", "os.ReadDir"}:                             {1, "outside: setup's adoption"},
	{"cmd/jevlin/setup_adopt.go", "setAsideInstallation", "os.ReadDir"}:                 {1, "outside: lists the installation home's parent"},
	{"cmd/jevlin/setup_config.go", "publishSetupConfig", "fsx.WriteFileAtomic"}:         {1, "outside: jevlin.toml"},
	{"cmd/jevlin/setup_config.go", "readExistingConfig", "os.ReadFile"}:                 {1, "outside: jevlin.toml"},
	{"cmd/jevlin/setup_env.go", "applyUserEnvironment", "fsx.WriteFileAtomic"}:          {1, "outside: the shell profile or the environment journal"},
	{"cmd/jevlin/setup_env.go", "applyUserEnvironmentRevert", "os.Remove"}:              {1, "outside: the environment journal"},
	{"cmd/jevlin/setup_env.go", "profileTarget", "os.ReadFile"}:                         {1, "outside: the shell profile"},
	{"cmd/jevlin/setup_env.go", "readEnvJournal", "os.ReadFile"}:                        {1, "outside: the environment journal"},
	{"cmd/jevlin/setup_env_unix.go", "restrictToOwner", "os.Chmod"}:                     {2, "root: chmods the home and the roots themselves (by path, on a directory), never a path below one"},
	{"cmd/jevlin/setup_env_unix.go", "setupRun.environmentStep", "fsx.WriteFileAtomic"}: {1, "outside: the shell profile"},
	{"cmd/jevlin/turn_end.go", "cmdTurnEnd", "fsx.ReadRegular"}:                         {1, "root: the queued turn end, read without waiting and bounded"},
	{"cmd/jevlin/update_check.go", "readUpdateStampFile", "fsx.ReadRegular"}:            {1, "outside: the update stamp beside credentials.json, in the installation's own root; read bounded, and every word in it held to a token on load"},
	{"cmd/jevlin/update_check.go", "writeUpdateStamp", "fsx.WriteFileAtomic"}:           {1, "outside: the update stamp beside credentials.json, in the installation's own root; fsx stages under an exclusive random name"},
	{"cmd/jevlin/turn_end.go", "cmdTurnEnd", "os.Remove"}:                               {1, "root: removes the queued turn end's name"},
	{"cmd/jevlin/turn_end.go", "sweepTurnMarks", "os.ReadDir"}:                          {1, "root: lists the sessions dir's top level; O_DIRECTORY refuses a FIFO"},
	{"cmd/jevlin/turn_end.go", "sweepTurnMarks", "os.Remove"}:                           {1, "root: removes stale turn marks by name"},
	{"cmd/jevlin/uninstall.go", "readRemoved", "os.ReadDir"}:                            {1, "outside: a host's skill directory"},
	{"cmd/jevlin/uninstall.go", "uninstallRun.applyBinary", "os.ReadDir"}:               {1, "outside: the binary's directory"},
	{"cmd/jevlin/uninstall.go", "uninstallRun.applyBinary", "os.Remove"}:                {2, "outside: the binary"},
	{"cmd/jevlin/uninstall.go", "uninstallRun.applyBinary", "os.RemoveAll"}:             {1, "outside: the binary's directory"},
	{"cmd/jevlin/uninstall.go", "uninstallRun.applyEnvironment", "fsx.WriteFileAtomic"}: {1, "outside: the shell profile"},
	{"cmd/jevlin/uninstall.go", "uninstallRun.applyPurge", "os.RemoveAll"}:              {1, "root: purge removes the directories; RemoveAll removes a link, never what it points at"},
	{"cmd/jevlin/uninstall.go", "uninstallRun.closing", "os.ReadDir"}:                   {1, "outside: the installation home"},
	{"cmd/jevlin/uninstall.go", "uninstallRun.purgeSet", "os.ReadDir"}:                  {1, "outside: the installation home"},
	{"cmd/jevlin/uninstall.go", "uninstallRun.removeHomeIfEmpty", "os.ReadDir"}:         {1, "outside: the installation home"},
	{"cmd/jevlin/uninstall.go", "uninstallRun.removeHomeIfEmpty", "os.Remove"}:          {1, "outside: the installation directory"},
	{"internal/selfupdate/candidate.go", "StageCandidate", "fsx.SyncDirectory"}:         {1, "outside: the binary's directory"},
	{"internal/selfupdate/candidate.go", "StageCandidate", "os.CreateTemp"}:             {1, "outside: the binary's directory"},
	{"internal/selfupdate/candidate.go", "StageCandidate", "os.Remove"}:                 {1, "outside: the binary's directory"},
	{"internal/selfupdate/prepare.go", "Prepared.Discard", "os.Remove"}:                 {1, "outside: the binary's directory"},
	{"internal/selfupdate/prepare.go", "Prepared.DiscardAfterInstall", "os.Remove"}:     {1, "outside: the binary's directory"},
	{"internal/selfupdate/rename_unix.go", "platformRenameNew", "os.Rename"}:            {1, "outside: the binary's directory"},
	{"internal/selfupdate/rename_unix.go", "platformRenameReplace", "os.Rename"}:        {1, "outside: the binary's directory"},
	{"internal/selfupdate/rename_windows.go", "moveFileEx", "windows.MoveFileEx"}:       {1, "outside: the binary's directory"},
	{"internal/selfupdate/replace.go", "StagingLeftovers", "os.ReadDir"}:                {1, "outside: the binary's directory"},
	{"internal/selfupdate/replace.go", "durableSnapshot", "fsx.SyncDirectory"}:          {1, "outside: the binary's directory"},
	{"internal/selfupdate/replace.go", "durableSnapshot", "os.CreateTemp"}:              {1, "outside: the binary's directory"},
	{"internal/selfupdate/replace.go", "durableSnapshot", "os.Open"}:                    {1, "outside: the binary"},
	{"internal/selfupdate/replace.go", "durableSnapshot", "os.Remove"}:                  {1, "outside: the binary's directory"},
	{"internal/selfupdate/replace.go", "replaceOpsWithin", "fsx.SyncDirectory"}:         {1, "outside: the binary's directory"},
	{"internal/selfupdate/replace.go", "replaceOpsWithin", "os.Remove"}:                 {1, "outside: the binary's directory"},
	{"internal/selfupdate/replace.go", "reservePath", "os.CreateTemp"}:                  {1, "outside: the binary's directory"},
	{"internal/selfupdate/replace.go", "reservePath", "os.Remove"}:                      {1, "outside: the binary's directory"},
	{"internal/selfupdate/rollback.go", "Updater.Rollback", "os.Remove"}:                {1, "outside: the binary's directory"},
	{"internal/selfupdate/rollback.go", "readPrevious", "os.Open"}:                      {1, "outside: the previous binary"},
	{"internal/winacl/winacl_windows.go", "openNoFollow", "windows.CreateFile"}:         {1, "root and outside: the state dir, for doctor; READ_CONTROL and FILE_READ_ATTRIBUTES only, FILE_FLAG_OPEN_REPARSE_POINT, the attributes checked on the handle, every share mode so it never makes a writer wait"},
	{"pkg/auth/store.go", "OpenStore", "os.MkdirAll"}:                                   {1, "root: creates the state dir itself"},
	{"pkg/auth/store.go", "undoCreated", "os.Remove"}:                                   {1, "root: removes, innermost first, the directories OpenStore created a moment ago when it could not make or restrict them; Remove follows no link and fails on a directory that is no longer empty"},
	{"pkg/auth/store.go", "RegistrationJournal.Clear", "os.Remove"}:                     {1, "outside: the journal's name in the installation's own directory, which agents install keeps out of every writable root; read through readSecret first"},
	{"pkg/auth/store.go", "RegistrationJournal.Save", "os.MkdirAll"}:                    {1, "outside: creates the installation's own directory, beside credentials.json"},
	{"pkg/auth/claim_record.go", "ClaimRecord.Save", "os.MkdirAll"}:                     {1, "outside: creates the installation's own directory, beside credentials.json; claim.json is written there through fsx.WriteFileAtomic and read through readSecret"},
	{"pkg/auth/store.go", "Store.DiscardLegacyPendingRegistration", "os.Remove"}:        {1, "root: removes the legacy journal's name in the state dir, never read"},
	{"pkg/auth/store.go", "Store.setAsideAgentRegistration", "os.Rename"}:               {1, "root: renames agent.json aside; a rename replaces the name, never what it pointed at"},
	{"pkg/auth/store.go", "Store.createExclusive", "fsx.WriteFileExclusive"}:            {1, "root: staged under an exclusive random name, published without replacing"},
	{"pkg/auth/store.go", "Store.readSecret", "fsx.ReadRegularNoFollow"}:                {1, "root: the state dir's records, one no-follow open, mode checked on it"},
	{"pkg/auth/store.go", "Store.saveStateFile", "fsx.WriteFileAtomic"}:                 {1, "root: staged under an exclusive random name"},
	{"pkg/config/config.go", "Load", "os.ReadFile"}:                                     {1, "outside: the config file"},
	{"pkg/fsx/atomic.go", "defaultOperations", "os.CreateTemp"}:                         {1, "fsx: the exclusive random staging name"},
	{"pkg/fsx/atomic.go", "defaultOperations", "os.Remove"}:                             {1, "fsx: removes its own staging name"},
	{"pkg/fsx/replace_file.go", "replaceFile", "os.OpenFile"}:                           {1, "fsx: opens an existing regular file for writing without truncating, only to refuse one the caller could not write in place; closed unwritten"},
	{"pkg/fsx/confined.go", "CreateNew", "os.OpenFile"}:                                 {1, "fsx: O_EXCL, never an existing name"},
	{"pkg/fsx/confined.go", "CreateNew", "os.Remove"}:                                   {1, "fsx: removes the file it just created"},
	{"pkg/fsx/confined_unix.go", "openLock", "os.OpenFile"}:                             {1, "fsx: O_NOFOLLOW, then a regular-file check"},
	{"pkg/fsx/confined_unix.go", "openLockExisting", "os.OpenFile"}:                     {1, "fsx: O_NOFOLLOW and O_NONBLOCK, then a regular-file check"},
	{"pkg/fsx/confined_unix.go", "openNoFollow", "os.OpenFile"}:                         {1, "fsx: O_NOFOLLOW and O_NONBLOCK, then a regular-file check"},
	{"pkg/fsx/confined_unix.go", "openNoWait", "os.OpenFile"}:                           {1, "fsx: O_NONBLOCK, then a regular-file check"},
	{"pkg/fsx/confined_windows.go", "openExclusive", "windows.CreateFile"}:              {1, "fsx: FILE_FLAG_OPEN_REPARSE_POINT, then an attribute check"},
	{"pkg/fsx/confined_windows.go", "openNoFollow", "windows.CreateFile"}:               {1, "fsx: FILE_FLAG_OPEN_REPARSE_POINT, then an attribute check"},
	{"pkg/fsx/confined_windows.go", "openNoWait", "os.Open"}:                            {1, "fsx: then a regular-file check; no FIFO lives at a Windows name"},
	{"pkg/fsx/publication_unix.go", "movePublication", "os.Rename"}:                     {1, "fsx: the durable move its callers confine"},
	{"pkg/fsx/publication_unix.go", "publish", "os.Link"}:                               {1, "fsx: publishes its own staged file without replacing"},
	{"pkg/fsx/publication_unix.go", "publish", "os.Rename"}:                             {1, "fsx: publishes its own staged file"},
	{"pkg/fsx/publication_unix.go", "removeDurable", "os.Remove"}:                       {1, "fsx: removes a name"},
	{"pkg/fsx/publication_unix.go", "syncDirectory", "os.Open"}:                         {1, "fsx: opens a directory to fsync it"},
	{"pkg/fsx/publication_windows.go", "publish", "windows.MoveFileEx"}:                 {1, "fsx: publishes its own staged file, write-through"},
	{"pkg/fsx/publication_windows.go", "removeDurable", "os.CreateTemp"}:                {1, "fsx: the exclusive random staging name"},
	{"pkg/fsx/publication_windows.go", "removeDurable", "os.Remove"}:                    {1, "fsx: removes its own staging name"},
	{"pkg/fsx/root.go", "OpenRoot", "os.OpenRoot"}:                                      {1, "fsx: the only os.Root in the module; Lstat first, then SameFile on what opened"},
	{"pkg/fsx/root_windows.go", "Root.pinDir", "windows.CreateFile"}:                    {1, "fsx: pins a directory without FILE_SHARE_DELETE, checked against the root's own resolution"},
}

func TestEveryFileOperationIsClassifiedForTheWritableRoots(t *testing.T) {
	root := moduleRoot(t)
	found, dotImports, err := walkFileCalls(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dotImports {
		t.Errorf("dot import of a watched package hides its file operations from this guard: %s", d)
	}
	var unlisted, wrong []string
	for site, n := range found {
		c, ok := classifiedFileCalls[site]
		switch {
		case !ok:
			unlisted = append(unlisted, fmt.Sprintf("%s %s %s (x%d)", site.file, site.fn, site.call, n))
		case c.n != n:
			wrong = append(wrong, fmt.Sprintf("%s %s %s occurs %d times, listed as %d", site.file, site.fn, site.call, n, c.n))
		}
	}
	for site := range classifiedFileCalls {
		if found[site] == 0 {
			wrong = append(wrong, fmt.Sprintf("%s %s %s is listed and no longer there; remove the entry", site.file, site.fn, site.call))
		}
	}
	sort.Strings(unlisted)
	sort.Strings(wrong)
	for _, s := range unlisted {
		t.Errorf("unclassified file operation: %s\n\tIf it can touch the state or sessions directory, use pkg/fsx's confined operations"+
			" (CreateNew, ReadRegular, OpenLock, Root); then add it to classifiedFileCalls with the reason it is safe.", s)
	}
	for _, s := range wrong {
		t.Errorf("classifiedFileCalls is out of date: %s", s)
	}
	if len(found) == 0 {
		t.Fatal("the walk found no file operation at all; it is not looking where the code is")
	}
}

// walkFileCalls counts the watched references in every non-test Go file
// under root. It skips directories whose names start with "." or "_" (a
// worktree, an editor's state), testdata, node_modules, bin and dist at any
// depth, and tools only at the module root.
func walkFileCalls(root string) (map[fileCallSite]int, []string, error) {
	fset := token.NewFileSet()
	found := map[fileCallSite]int{}
	var dots []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == root {
				return nil
			}
			n := d.Name()
			switch {
			case strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_"),
				n == "testdata" || n == "node_modules" || n == "bin" || n == "dist",
				n == "tools" && filepath.Dir(p) == root:
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", p, perr)
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		local := map[string]string{}
		for _, imp := range file.Imports {
			ip, uerr := strconv.Unquote(imp.Path.Value)
			if uerr != nil || watchedFileCalls[ip] == nil {
				continue
			}
			name := path.Base(ip)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			if name == "." {
				dots = append(dots, rel+" imports "+ip)
				continue
			}
			local[name] = ip
		}
		for _, decl := range file.Decls {
			fn := enclosingName(decl)
			ast.Inspect(decl, func(n ast.Node) bool {
				se, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				x, ok := se.X.(*ast.Ident)
				if !ok {
					return true
				}
				if ip, ok := local[x.Name]; ok && watchedFileCalls[ip][se.Sel.Name] {
					found[fileCallSite{rel, fn, path.Base(ip) + "." + se.Sel.Name}]++
				}
				return true
			})
		}
		return nil
	})
	return found, dots, err
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
