package auth

// Who may open the state directory and its files.
//
// On POSIX that is file modes, and the store checks them on every open and
// every load (store.go): a group- or world-accessible secret refuses mining
// startup. On Windows Go's modes say nothing (posixModes is false) and access
// is a DACL, which is inherited: a state_dir made outside `jevlin setup`, or
// beneath a parent that later gains an inheritable entry, admits every
// principal its parent does. So on Windows the protection is applied once, when
// OpenStore creates the directory (perm_windows.go), and the store never reads
// an access list afterwards.
//
// That last part is a decision, not a gap. The state directory is a writable
// root of Codex's sandbox (codexSandboxRoots), docs/agents.md tells the
// participant that sandboxed commands read it, and a sandbox's setup grants its
// own group an entry on it — so another principal on the list is the intended
// state of an installation that uses one. A refusal on "any entry for anyone
// but you, SYSTEM and Administrators" would then fail on every open: mining
// DEGRADED after every search, and connect, status and doctor refusing the
// store they exist to repair or report. A check on every load would also read
// a DACL, and name its principals, on the search path (AGENTS.md invariant 1).
// What the participant is told about who else can open the directory is
// doctor's `state access` line (cmd/jevlin/state_acl.go), which reads and
// reports and changes nothing.
