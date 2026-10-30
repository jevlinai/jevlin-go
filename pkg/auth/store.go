package auth

// Secret custody per ADR-0008: owner-only files in a 0700 state
// directory. The store owns the installation's state: the trace key and
// the platform registration in the state directory, and the records that
// sit beside credentials.json outside it. Every load re-verifies
// permissions and refuses group/world-accessible or symlinked paths.

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/fsx"
)

const (
	registrationPendingFile = "registration_pending.json"
	traceKeyFile            = "trace.key"
	traceKeyLen             = 32
)

// agentRegistrationFile holds the platform identity. Its claim code moved to
// the claim record beside credentials.json, outside the state directory.
const agentRegistrationFile = "agent.json"

// CredentialFiles names the files in the state directory whose content
// authorizes someone or keys something: the trace key, and the agent
// registration, which names this installation's platform identity. It is a
// closed list on purpose. doctor reports who else can open them, and a
// directory a sandbox can write is not one to enumerate, so the report asks
// for these by name. A file added to the store is held to this list by
// credential_files_test.go, which fails until it is classified here or as a
// record.
func CredentialFiles() []string {
	return []string{traceKeyFile, agentRegistrationFile}
}

// ErrAgentRegistrationCorrupt identifies an undecodable agent.json whose
// contents may still represent a live platform identity.
var ErrAgentRegistrationCorrupt = errors.New("auth: agent registration is corrupt")

// Store is the per-installation state directory.
type Store struct {
	dir string
}

// OpenStore creates or opens the state directory ([search] state_dir):
// created 0700 (on Windows, every directory this call creates is restricted
// to its owner as it is created and none is judged afterwards: perm.go), and
// refused if it is a symlink or group/world-accessible.
func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("auth: state_dir is empty")
	}
	// MkdirAll can make several levels, and on Windows each inherits its
	// parent's list: restricting only the leaf would leave a D:\jevlin this
	// call made with D:\'s entries, from where another account can rename the
	// restricted state dir away and plant its own. So the levels that do not
	// exist are recorded first and every one of them is restricted.
	made := missingDirs(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil { // #nosec G703 -- operator-configured state dir, validated just below
		return nil, fmt.Errorf("auth: create state dir: %w%s", err, undoCreated(made))
	}
	// Before anything is written into any of them, outermost first.
	for _, d := range made {
		if err := restrictCreatedStateDir(d); err != nil {
			// Left in place, the next OpenStore would find a directory that
			// exists, take it as it stands and never restrict it: one failed
			// call would leave the secrets under the parent's list for good.
			// They were made a moment ago and nothing was written into them,
			// so they go, and the next run restricts what it creates.
			return nil, fmt.Errorf("auth: restrict state dir %s to its owner: %w%s", d, err, undoCreated(made))
		}
	}
	return openExistingStore(dir)
}

// missingDirs lists the directories on the way to dir, dir included, that do
// not exist yet, outermost first.
func missingDirs(dir string) []string {
	var missing []string
	for p := filepath.Clean(dir); ; {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) { // #nosec G703 -- operator-configured state dir
			break // there, or not something to make
		}
		missing = append(missing, p)
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	slices.Reverse(missing)
	return missing
}

// undoCreated takes away, innermost first, the directories a failed OpenStore
// made, and returns what to append to its error when one could not go (a
// directory that is no longer empty stays, and is said). A directory that is
// already gone is not a failure.
func undoCreated(made []string) string {
	var stuck []string
	for i := len(made) - 1; i >= 0; i-- {
		if err := os.Remove(made[i]); err != nil && !errors.Is(err, fs.ErrNotExist) { // #nosec G703 -- directories this call created
			stuck = append(stuck, made[i])
		}
	}
	if len(stuck) == 0 {
		return ""
	}
	return fmt.Sprintf(" (and %s could not be removed to try again)", strings.Join(stuck, ", "))
}

// OpenStoreExisting opens an already-existing state directory without
// creating anything. It is the inspection path for commands such as doctor
// and status, and for search-side state reads: diagnosis must not manufacture
// custody state merely because a participant asked a question.
func OpenStoreExisting(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("auth: state_dir is empty")
	}
	return openExistingStore(dir)
}

func openExistingStore(dir string) (*Store, error) {
	info, err := os.Lstat(dir) // #nosec G703 -- same validated operator path
	if err != nil {
		return nil, fmt.Errorf("auth: stat state dir: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("auth: state dir is a symlink; refusing")
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("auth: state dir is not a directory")
	}
	if posixModes && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("auth: state dir is group/world-accessible (%04o); refusing", info.Mode().Perm())
	}
	return &Store{dir: dir}, nil
}

// storeFileMaxBytes bounds every file read back from the state dir. Its
// records and secrets are each a few kilobytes at most.
const storeFileMaxBytes = 1 << 20

// secretCheckedHook runs between readSecret's checks on the name and its
// read, when a test sets it: the seam a test uses to replace the file at
// exactly that moment. Nil in production.
var secretCheckedHook func(path string)

// readSecret loads a secret file, re-verifying on EVERY load that it is
// a regular, owner-only file (ADR-0008).
func (s *Store) readSecret(name string) ([]byte, error) {
	path := filepath.Join(s.dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err // fs.ErrNotExist flows through for first-use
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("auth: %s is a symlink; refusing", name)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("auth: %s is not a regular file; refusing", name)
	}
	if posixModes && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("auth: %s is group/world-accessible (%04o); refusing", name, info.Mode().Perm())
	}
	if secretCheckedHook != nil {
		secretCheckedHook(path)
	}
	// The checks above name the file; the state dir is a writable root of
	// Codex's sandbox, so the name can be replaced before the read. The read
	// opens it once, refusing a link and not waiting on a FIFO, and the
	// type and mode are checked again on what was opened.
	raw, held, err := fsx.ReadRegularNoFollow(path, storeFileMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("auth: read %s: %w", name, err)
	}
	if posixModes && held.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("auth: %s is group/world-accessible (%04o); refusing", name, held.Mode().Perm())
	}
	return raw, nil
}

// createExclusive writes a brand-new secret file 0600 via O_CREAT|O_EXCL
// — first generation must never clobber concurrent creation.
func (s *Store) createExclusive(name string, data []byte) error {
	return fsx.WriteFileExclusive(s.dir, name, data, 0o600)
}

// saveStateFile durably REPLACES name's content, and is the only way any
// mutable state file in this store is written: a random-suffixed temp file
// via CreateTemp (never a fixed name — a fixed name is itself a race
// between two writers, and a leftover from a killed process would
// otherwise get silently published by the next successful write), 0600
// before any content lands in it, fsync'd, then renamed over the final
// name. Any failure removes the temp file rather than leaving it for a
// later rename to publish by accident.
func (s *Store) saveStateFile(name string, data []byte) error {
	return fsx.WriteFileAtomic(s.dir, name, data, 0o600)
}

// TraceKey loads the installation's 32-byte trace key, generating it on
// first use (0600, exclusive-create). It keys the trace identifiers derived
// from guessable local facts (hostname, parent pid) so the router that
// receives them cannot enumerate them back. The key never leaves the machine.
func (s *Store) TraceKey() ([]byte, error) {
	key, _, err := s.traceKey()
	return key, err
}

// LoadTraceKey reports whether the trace key is on file, without making
// one: doctor's question, which must leave the directory as it found it.
// A key of the wrong length is an error, as it is for TraceKey.
func (s *Store) LoadTraceKey() (present bool, err error) {
	raw, err := s.readSecret(traceKeyFile)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(raw) != traceKeyLen {
		return false, fmt.Errorf("auth: %s is %d bytes, want %d; refusing", traceKeyFile, len(raw), traceKeyLen)
	}
	return true, nil
}

// EnsureTraceKey makes the trace key exist and reports whether this call
// made it. Setup and connect call it because they run outside any sandbox
// and already write this directory; a search that cannot write here then
// finds the key and only has to read it (TraceKey still creates one where
// it can, for an installation that predates the call).
func (s *Store) EnsureTraceKey() (created bool, err error) {
	_, created, err = s.traceKey()
	return created, err
}

// TraceKeyPath is where the trace key of the state directory dir lives, for
// a caller that wants to say so without opening the store.
func TraceKeyPath(dir string) string { return filepath.Join(dir, traceKeyFile) }

func (s *Store) traceKey() (key []byte, created bool, err error) {
	raw, err := s.readSecret(traceKeyFile)
	if errors.Is(err, fs.ErrNotExist) {
		key := make([]byte, traceKeyLen)
		if _, err := rand.Read(key); err != nil {
			return nil, false, fmt.Errorf("auth: generate %s: %w", traceKeyFile, err)
		}
		err = s.createExclusive(traceKeyFile, key)
		if err == nil {
			return key, true, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, false, err
		}
		// Another process created it first; its key is the one to use.
		raw, err = s.readSecret(traceKeyFile)
	}
	if err != nil {
		return nil, false, err
	}
	if len(raw) != traceKeyLen {
		return nil, false, fmt.Errorf("auth: %s is %d bytes, want %d; refusing", traceKeyFile, len(raw), traceKeyLen)
	}
	return raw, false, nil
}

// AgentRegistration is this installation's identity on the search
// platform (agent onboarding design §3): what register minted and what the
// claim did to it. It is the client's cache of the platform's own state,
// re-verified against GET /v1/agents/{id} on every poll — never trusted as
// an authority on its own, only as what to resume from.
//
// It carries no claim link. Older versions kept claim_url and claim_code
// here, and the fields of a mining enrollment (last_enrollment_slot,
// last_enrollment_at, slot_refusal, offered_slots); they decode as unknown
// fields and are ignored, because this file is in a state directory a
// sandboxed command can rewrite and the link is what a person is told to
// open. The link lives in ClaimRecord, beside credentials.json.
type AgentRegistration struct {
	AgentID        string   `json:"agent_id"`
	Status         string   `json:"status"` // "unclaimed" | "claimed" | "expired"
	Scopes         []string `json:"scopes,omitempty"`
	ClaimExpiresAt string   `json:"claim_expires_at,omitempty"`
}

// SaveAgentRegistration persists the platform identity, overwriting
// whatever was there. This record legitimately advances through unclaimed
// -> claimed, so it is write-and-rename rather than createExclusive. A
// record LoadAgentRegistration would refuse is not written.
func (s *Store) SaveAgentRegistration(rec AgentRegistration) error {
	if err := checkAgentRegistrationShapes(rec); err != nil {
		return fmt.Errorf("auth: refusing to store an agent registration: %w", err)
	}
	if field := recordTextProblem(rec); field != "" {
		return fmt.Errorf("auth: refusing to store an agent registration whose %s holds a control, format or separator character", field)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("auth: encode agent registration: %w", err)
	}
	return s.saveStateFile(agentRegistrationFile, raw)
}

// LoadAgentRegistration returns the stored platform identity, ok=false
// when this installation has never registered.
//
// agent.json is in the state directory, which a sandboxed command can
// rewrite (record_text.go), and its strings reach a terminal: status prints
// the scopes. A record holding a control, format or separator character in
// any string, or an identifier that is not a token (agent_id.go), is
// ErrAgentRegistrationCorrupt, exactly like one that does not decode, so
// the one path that already handles a record that cannot be trusted
// handles this one too: a foreground connect sets it aside and rebuilds it
// from GET /v1/agents/me, and nothing else acts on it. The error names the
// field and never repeats its bytes.
func (s *Store) LoadAgentRegistration() (rec AgentRegistration, ok bool, err error) {
	raw, err := s.readSecret(agentRegistrationFile)
	if errors.Is(err, fs.ErrNotExist) {
		return AgentRegistration{}, false, nil
	}
	if err != nil {
		return AgentRegistration{}, false, err
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return AgentRegistration{}, false, fmt.Errorf("%w: %s", ErrAgentRegistrationCorrupt, decodeProblem(err))
	}
	if field := recordTextProblem(rec); field != "" {
		return AgentRegistration{}, false, fmt.Errorf("%w: its %s holds a control, format or separator character", ErrAgentRegistrationCorrupt, field)
	}
	if err := checkAgentRegistrationShapes(rec); err != nil {
		return AgentRegistration{}, false, fmt.Errorf("%w: %v", ErrAgentRegistrationCorrupt, err)
	}
	return rec, true, nil
}

// PreserveCorruptAgentRegistration moves an undecodable agent record aside
// without deleting it. Callers use this only after deciding a replacement is
// permitted: either a fresh Register with no existing platform key to
// conflict with, or — since the router's GET /v1/agents/me self-lookup let a
// corrupt record be rebuilt from that key instead of refused (agent
// onboarding design rule 8 amendment, PR B) — a caller that has already
// confirmed the key still names a real identity there. An existing platform
// key no longer forces a refusal on its own; it only means the rename must
// wait for that confirmation first.
//
// It moves only a regular file: the record it was asked to preserve is one
// that was read and found corrupt, and anything else at the name since is
// not that record.
func (s *Store) PreserveCorruptAgentRegistration() error {
	return s.setAsideAgentRegistration("corrupt", false)
}

// SetAsideAgentRegistration moves a readable agent record that names a
// different identity from the one being published aside, without deleting
// it: the registration journal and the credential beside it, both outside
// the state directory, have decided which agent this installation is.
func (s *Store) SetAsideAgentRegistration() error {
	return s.setAsideAgentRegistration("replaced", true)
}

// SetAsideUnreadableAgentRegistration moves aside whatever is at agent.json
// that would not load: a link, a directory, a FIFO or a file others can
// read, any of which a sandboxed command can leave there. Publication calls
// it once credentials.json holds the registration journal's key, when the
// journal has decided which agent this installation is.
func (s *Store) SetAsideUnreadableAgentRegistration() error {
	return s.setAsideAgentRegistration("unreadable", true)
}

// setAsideAgentRegistration renames agent.json to agent.json.<why>. With
// anyShape it moves whatever is there: a record, or a link, a directory, a
// FIFO or a file others can read, which a sandboxed command can leave at
// that name. A rename moves a name and never opens or follows what it names,
// so none of those is a reason to refuse where the journal has already
// decided which agent this is; refusing, as this once did, left that
// registration wedged behind whatever was planted. Without anyShape only a
// regular file is moved. An earlier copy at the backup name is replaced: it is evidence
// of an earlier record in a directory a sandboxed command can write, and
// refusing on it made every later rebuild fail the same way. Where either
// name holds anything but a regular file the backup gets a fresh,
// timestamped name instead, since a rename cannot put a directory over a
// file or a file over a directory.
func (s *Store) setAsideAgentRegistration(why string, anyShape bool) error {
	path := filepath.Join(s.dir, agentRegistrationFile)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !anyShape && !info.Mode().IsRegular() {
		return errors.New("auth: agent registration is not a regular file")
	}
	backup := filepath.Join(s.dir, "agent.json."+why)
	if held, err := os.Lstat(backup); err == nil && (!held.Mode().IsRegular() || !info.Mode().IsRegular()) {
		backup = fmt.Sprintf("%s-%d", backup, time.Now().UnixNano())
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(path, backup); err != nil {
		return fmt.Errorf("auth: set the agent registration aside: %w", err)
	}
	return nil
}

// PendingRegistration is the durable hand-off between a validated remote
// Register response and the local publication of the platform key and agent
// record. It is intentionally separate from AgentRegistration: the key is
// secret material and this record only exists while local publication is
// incomplete.
type PendingRegistration struct {
	V               int    `json:"v"`
	AgentID         string `json:"agent_id"`
	Key             string `json:"key"`
	ClaimURL        string `json:"claim_url"`
	ClaimCode       string `json:"claim_code"`
	ClaimExpiresAt  string `json:"claim_expires_at"`
	Status          string `json:"status"`
	PollIntervalMS  int64  `json:"poll_interval_ms,omitempty"`
	ReplaceExpired  bool   `json:"replace_expired,omitempty"`
	PreviousAgentID string `json:"previous_agent_id,omitempty"`
	PreviousKey     string `json:"previous_key,omitempty"`
	// Router and AgentsAPI are the origins (scheme://host[:port]) the key
	// is for: the router and the agents API of the config that registered
	// it. Publication records them with the key. A journal an older client
	// wrote carries neither.
	Router    string `json:"router,omitempty"`
	AgentsAPI string `json:"agents_api,omitempty"`
}

const pendingRegistrationVersion = 1

// RegistrationJournal holds registration_pending.json. It is not part of
// the state directory: a sandboxed agent may write [search] state_dir, and
// the journal carries the authority to publish a platform key over
// credentials.json (replace_expired, previous_key). It lives beside
// credentials.json, so whoever can forge it could already rewrite the
// credential itself.
type RegistrationJournal struct {
	dir string
}

// OpenRegistrationJournal names the journal in dir, the directory holding
// credentials.json. Nothing is created until Save.
func OpenRegistrationJournal(dir string) (*RegistrationJournal, error) {
	if dir == "" {
		return nil, errors.New("auth: registration journal directory is empty")
	}
	return &RegistrationJournal{dir: dir}, nil
}

func (j *RegistrationJournal) store() *Store { return &Store{dir: j.dir} }

func (j *RegistrationJournal) Save(rec PendingRegistration) error {
	rec.V = pendingRegistrationVersion
	if err := validatePendingRegistration(rec); err != nil {
		return err
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("auth: encode pending registration: %w", err)
	}
	if err := os.MkdirAll(j.dir, 0o700); err != nil { // #nosec G703 -- the jevlin home, beside credentials.json
		return fmt.Errorf("auth: create registration journal dir: %w", err)
	}
	return j.store().saveStateFile(registrationPendingFile, raw)
}

func (j *RegistrationJournal) Load() (rec PendingRegistration, ok bool, err error) {
	raw, err := j.store().readSecret(registrationPendingFile)
	if errors.Is(err, fs.ErrNotExist) {
		return PendingRegistration{}, false, nil
	}
	if err != nil {
		return PendingRegistration{}, false, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return PendingRegistration{}, false, &decodeError{what: "pending registration", err: err}
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return PendingRegistration{}, false, errors.New("auth: decode pending registration: trailing data")
	}
	if err := validatePendingRegistration(rec); err != nil {
		return PendingRegistration{}, false, err
	}
	return rec, true, nil
}

// Clear removes the transient journal only after both local publication
// targets have been verified. Reuse readSecret first so a symlink or unsafe
// journal is never removed as if it were our state file.
func (j *RegistrationJournal) Clear() error {
	if _, err := j.store().readSecret(registrationPendingFile); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Remove(filepath.Join(j.dir, registrationPendingFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("auth: clear pending registration: %w", err)
	}
	return nil
}

// DiscardLegacyPendingRegistration removes a registration_pending.json from
// the state directory, where releases before RegistrationJournal kept it. It
// is never read: anything in the state directory may have been written by a
// sandboxed command. Only the name itself is removed, never a link target.
func (s *Store) DiscardLegacyPendingRegistration() (bool, error) {
	path := filepath.Join(s.dir, registrationPendingFile)
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("auth: stat legacy pending registration: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return true, fmt.Errorf("auth: discard legacy pending registration: %w", err)
	}
	return true, nil
}

func validatePendingRegistration(rec PendingRegistration) error {
	if rec.V != pendingRegistrationVersion {
		return fmt.Errorf("auth: pending registration has unsupported version %d", rec.V)
	}
	if !boundedNonEmpty(rec.AgentID, 1, 512) || !boundedNonEmpty(rec.Key, 1, 4096) ||
		!boundedNonEmpty(rec.ClaimURL, 1, 4096) || ValidClaimCode(rec.ClaimCode) != nil ||
		!bounded(rec.ClaimExpiresAt, 0, 128) || rec.Status != "unclaimed" ||
		rec.PollIntervalMS < 0 || rec.PollIntervalMS > int64((24*time.Hour)/time.Millisecond) ||
		(rec.ReplaceExpired && (!boundedNonEmpty(rec.PreviousAgentID, 1, 512) || !boundedNonEmpty(rec.PreviousKey, 1, 4096))) ||
		(!rec.ReplaceExpired && (rec.PreviousAgentID != "" || rec.PreviousKey != "")) ||
		!originOrEmpty(rec.Router) || !originOrEmpty(rec.AgentsAPI) {
		return errors.New("auth: pending registration fields are invalid")
	}
	return nil
}

// originOrEmpty: is value empty, or an http(s) origin and nothing more?
func originOrEmpty(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 512 {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil &&
		u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.Opaque == "" && value == u.Scheme+"://"+u.Host
}

func bounded(value string, min, max int) bool {
	n := len(value)
	return n >= min && n <= max
}

func boundedNonEmpty(value string, min, max int) bool {
	return bounded(value, min, max)
}
