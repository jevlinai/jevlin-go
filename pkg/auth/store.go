package auth

// Secret custody per ADR-0008: owner-only files in a 0700 state
// directory. The store owns the auth-plane material — the DPoP
// installation key and the renewable refresh authorization. Every load
// re-verifies permissions and refuses group/world-accessible or
// symlinked paths: custody failures close the MINING plane, never
// inference (the boundary tests keep this package off the request path).

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/fsx"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/jevlinai/jevlin-go/pkg/mining/draw"
)

const (
	dpopKeyFile             = "dpop.key"
	participationSecretFile = "participation.secret"
	refreshTokenFile        = "refresh.token"
	registrationPendingFile = "registration_pending.json"
	payoutRecordFile        = "payout.json"
	traceKeyFile            = "trace.key"
	traceKeyLen             = 32
)

// ErrAgentRegistrationCorrupt identifies an undecodable agent.json whose
// contents may still represent a live platform identity.
var ErrAgentRegistrationCorrupt = errors.New("auth: agent registration is corrupt")

// Store is the per-installation secret directory.
type Store struct {
	dir string
}

// OpenStore creates or opens the state directory ([mining] state_dir):
// created 0700, and refused if it is a symlink or group/world-accessible
// — the same hazard discipline as the Unix-socket listener.
func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("auth: state_dir is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil { // #nosec G703 -- operator-configured state dir, validated just below
		return nil, fmt.Errorf("auth: create state dir: %w", err)
	}
	return openExistingStore(dir)
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

// DPoPKey loads the installation's DPoP private key, generating it on
// first use (contract §15: created at first authorization setup, local
// to the installation forever). ES256 (ECDSA P-256) per the X-0002
// interoperability profile; PKCS#8 PEM on disk.
func (s *Store) DPoPKey() (*ecdsa.PrivateKey, error) {
	return s.dpopKey(true)
}

// DPoPKeyExisting loads the installation key without generating one. This is
// the only key path available to observational commands; a missing key means
// local authorization is incomplete, not that diagnosis should create it.
func (s *Store) DPoPKeyExisting() (*ecdsa.PrivateKey, error) {
	return s.dpopKey(false)
}

func (s *Store) dpopKey(generate bool) (*ecdsa.PrivateKey, error) {
	raw, err := s.readSecret(dpopKeyFile)
	switch {
	case err == nil:
		block, _ := pem.Decode(raw)
		if block == nil || block.Type != "PRIVATE KEY" {
			return nil, errors.New("auth: dpop.key is not a PKCS#8 PEM private key")
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("auth: parse dpop.key: %w", err)
		}
		key, ok := parsed.(*ecdsa.PrivateKey)
		if !ok || key.Curve != elliptic.P256() {
			return nil, errors.New("auth: dpop.key is not an ECDSA P-256 key (X-0002 profile requires ES256)")
		}
		return key, nil
	case errors.Is(err, fs.ErrNotExist) && generate:
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("auth: generate DPoP key: %w", err)
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("auth: encode DPoP key: %w", err)
		}
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		if err := s.createExclusive(dpopKeyFile, pemBytes); err != nil {
			return nil, err
		}
		return key, nil
	default:
		return nil, err
	}
}

// Thumbprint is the RFC 7638 JWK SHA-256 thumbprint of the key's public
// half, base64url — the `jkt` value the AS binds the installation to.
func Thumbprint(key *ecdsa.PrivateKey) (string, error) {
	tp, err := (&jose.JSONWebKey{Key: key.Public()}).Thumbprint(crypto.SHA256)
	if err != nil {
		return "", fmt.Errorf("auth: compute JWK thumbprint: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(tp), nil
}

// SaveRefreshToken durably replaces the renewable refresh authorization
// (rotation makes this a frequent, must-not-torn write): tmp file 0600
// in the same directory, then atomic rename.
func (s *Store) SaveRefreshToken(token string) error {
	if token == "" {
		return errors.New("auth: refusing to store an empty refresh token")
	}
	return fsx.WriteFileAtomic(s.dir, refreshTokenFile, []byte(token), 0o600)
}

// LoadRefreshToken returns the stored refresh authorization, or ok=false
// when none exists yet.
func (s *Store) LoadRefreshToken() (token string, ok bool, err error) {
	raw, err := s.readSecret(refreshTokenFile)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(raw), true, nil
}

// DeleteRefreshToken removes the refresh authorization (revocation /
// family-compromise handling).
func (s *Store) DeleteRefreshToken() error {
	err := os.Remove(filepath.Join(s.dir, refreshTokenFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("auth: delete refresh token: %w", err)
	}
	return nil
}

// storeFileMaxBytes bounds every file read back from the state dir. Its
// records and secrets are each a few kilobytes at most.
const storeFileMaxBytes = 1 << 20

// secretCheckedHook runs between readSecret's checks on the name and its
// read, when a test sets it: the seam a test uses to replace the file at
// exactly that moment. Nil in production.
var secretCheckedHook func(path string)

// readSecret loads a secret file, re-verifying on EVERY load that it is
// a regular, owner-only file (ADR-0008: a group/world-readable secret
// refuses mining startup).
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
		return nil, fmt.Errorf("auth: %s is group/world-accessible (%04o); refusing mining startup", name, info.Mode().Perm())
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
		return nil, fmt.Errorf("auth: %s is group/world-accessible (%04o); refusing mining startup", name, held.Mode().Perm())
	}
	return raw, nil
}

// createExclusive writes a brand-new secret file 0600 via O_CREAT|O_EXCL
// — first generation must never clobber concurrent creation.
func (s *Store) createExclusive(name string, data []byte) error {
	return fsx.WriteFileExclusive(s.dir, name, data, 0o600)
}

// saveStateFile durably REPLACES name's content — SaveRefreshToken's exact
// idiom (WP2-adversarial-review finding 5), and now the only way any
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

// ParticipationSecret loads the installation's 32-byte participation
// secret, generating it on first use (ADR-0008: participation.secret,
// 0600, exclusive-create; the derivation lives in internal/mining/draw
// and the raw bytes never leave the custody path).
func (s *Store) ParticipationSecret() (*draw.Secret, error) {
	raw, err := s.readSecret(participationSecretFile)
	switch {
	case err == nil:
		secret, err := draw.SecretFromBytes(raw)
		if err != nil {
			return nil, fmt.Errorf("auth: participation.secret: %w", err)
		}
		return secret, nil
	case errors.Is(err, fs.ErrNotExist):
		secret, err := draw.NewSecret()
		if err != nil {
			return nil, err
		}
		if err := s.createExclusive(participationSecretFile, secret.Bytes()); err != nil {
			return nil, err
		}
		return secret, nil
	default:
		return nil, err
	}
}

// TraceKey loads the installation's 32-byte trace key, generating it on
// first use (0600, exclusive-create). It keys the trace identifiers derived
// from guessable local facts (hostname, parent pid) so the router that
// receives them cannot enumerate them back. The key never leaves the machine.
func (s *Store) TraceKey() ([]byte, error) {
	key, _, err := s.traceKey()
	return key, err
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

// SaveReceipt persists an enrollment receipt's exact bytes (contract
// §22: the proxy retains the signed receipt as durable evidence).
func (s *Store) SaveReceipt(slotID, targetEpoch uint64, compactJWS string) error {
	name := fmt.Sprintf("receipt-%d-%d.jws", slotID, targetEpoch)
	if err := s.createExclusive(name, []byte(compactJWS)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			existing, rerr := s.readSecret(name)
			if rerr == nil && string(existing) == compactJWS {
				return nil // idempotent replay of identical bytes
			}
			return fmt.Errorf("auth: receipt for %d/%d already stored with different bytes", slotID, targetEpoch)
		}
		return err
	}
	return nil
}

// LoadReceipt returns the stored receipt bytes, ok=false when absent.
func (s *Store) LoadReceipt(slotID, targetEpoch uint64) (string, bool, error) {
	raw, err := s.readSecret(fmt.Sprintf("receipt-%d-%d.jws", slotID, targetEpoch))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(raw), true, nil
}

// AgentRegistration is this installation's identity on the search
// platform (agent onboarding design §3): what register minted, what the
// claim did to it, and what the last enrollment call recorded. It is the
// client's cache of the platform's own state, re-verified against
// GET /v1/agents/{id} on every poll — never trusted as an authority on
// its own, only as what to resume from.
//
// It carries no claim link. Older versions kept claim_url and claim_code
// here; they decode as unknown fields and are ignored, because this file is
// in a state directory a sandboxed command can rewrite and the link is what
// a person is told to open. The link lives in ClaimRecord, beside
// credentials.json.
type AgentRegistration struct {
	AgentID            string   `json:"agent_id"`
	Status             string   `json:"status"` // "unclaimed" | "claimed" | "expired"
	Scopes             []string `json:"scopes,omitempty"`
	ClaimExpiresAt     string   `json:"claim_expires_at,omitempty"`
	LastEnrollmentSlot string   `json:"last_enrollment_slot,omitempty"`
	LastEnrollmentAt   string   `json:"last_enrollment_at,omitempty"`
	// SlotRefusal is set when the platform offered more than one mining
	// slot and mining.platform_slot did not resolve one (WP2-adversarial-
	// review finding 15): persisted so shouldResume stops spawning a
	// resume that can only ever hit the same refusal again, and so status
	// can name it explicitly instead of the participant discovering it
	// only from a resume's silent no-op. Cleared the moment a slot
	// actually resolves (config changes, or the platform stops offering
	// more than one).
	SlotRefusal string `json:"slot_refusal,omitempty"`
}

// SaveAgentRegistration persists the platform identity, overwriting
// whatever was there. Like SaveEnrollment, this record legitimately
// advances through unclaimed -> claimed -> enrolled, so it is
// write-and-rename rather than createExclusive. A record
// LoadAgentRegistration would refuse is not written.
func (s *Store) SaveAgentRegistration(rec AgentRegistration) error {
	if err := ValidAgentID(rec.AgentID); err != nil {
		return fmt.Errorf("auth: refusing to store an agent registration: %w", err)
	}
	if field := recordTextProblem(rec); field != "" {
		return fmt.Errorf("auth: refusing to store an agent registration whose %s holds a control, C1 or bidi character", field)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("auth: encode agent registration: %w", err)
	}
	return s.saveStateFile("agent.json", raw)
}

// LoadAgentRegistration returns the stored platform identity, ok=false
// when this installation has never registered.
//
// agent.json is in the state directory, which a sandboxed command can
// rewrite (record_text.go), and every string in it reaches a terminal:
// status prints the claim link, the scopes, the slot and the refusal,
// connect the link and its code. A record holding a control, C1 or bidi
// character in any string is ErrAgentRegistrationCorrupt, exactly like one
// that does not decode, so the one path that already handles a record that
// cannot be trusted handles this one too: a foreground connect sets it aside
// and rebuilds it from GET /v1/agents/me, and nothing else acts on it. The
// error names the field and never repeats its bytes.
func (s *Store) LoadAgentRegistration() (rec AgentRegistration, ok bool, err error) {
	raw, err := s.readSecret("agent.json")
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
		return AgentRegistration{}, false, fmt.Errorf("%w: its %s holds a control, C1 or bidi character", ErrAgentRegistrationCorrupt, field)
	}
	if err := ValidAgentID(rec.AgentID); err != nil {
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
	path := filepath.Join(s.dir, "agent.json")
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
}

const pendingRegistrationVersion = 1

// RegistrationJournal holds registration_pending.json. It is not part of
// the state directory: a sandboxed agent may write [mining] state_dir, and
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
		return PendingRegistration{}, false, fmt.Errorf("auth: decode pending registration: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return PendingRegistration{}, false, errors.New("auth: decode pending registration: trailing data")
		}
		return PendingRegistration{}, false, fmt.Errorf("auth: decode pending registration: trailing data: %w", err)
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
		!boundedNonEmpty(rec.ClaimURL, 1, 4096) || !bounded(rec.ClaimCode, 0, 512) ||
		!bounded(rec.ClaimExpiresAt, 0, 128) || rec.Status != "unclaimed" ||
		rec.PollIntervalMS < 0 || rec.PollIntervalMS > int64((24*time.Hour)/time.Millisecond) ||
		(rec.ReplaceExpired && (!boundedNonEmpty(rec.PreviousAgentID, 1, 512) || !boundedNonEmpty(rec.PreviousKey, 1, 4096))) ||
		(!rec.ReplaceExpired && (rec.PreviousAgentID != "" || rec.PreviousKey != "")) {
		return errors.New("auth: pending registration fields are invalid")
	}
	return nil
}

func bounded(value string, min, max int) bool {
	n := len(value)
	return n >= min && n <= max
}

func boundedNonEmpty(value string, min, max int) bool {
	return bounded(value, min, max)
}

// validatePayoutAddress is the one place every payout address in this
// flow is checked (WP2-adversarial-review finding 9): a terminal-typed
// answer, mining.payout_address from config, and — redundantly but
// harmlessly, since it is already valid by construction — a freshly
// created wallet's own address all funnel through PayoutRecord.Save, so
// validating here structurally covers all three without relying on each
// call site to remember to. Every load of a record naming an address
// applies it again (#51).
func validatePayoutAddress(address string) error {
	hrp, _, err := DecodeBech32Address(address)
	if err != nil {
		return fmt.Errorf("auth: payout address %q does not decode as bech32: %w", address, err)
	}
	if hrp != TwilightHRP {
		return fmt.Errorf("auth: payout address %q has prefix %q, want %q", address, hrp, TwilightHRP)
	}
	return nil
}

// PayoutRecord holds payout.json, the payout address decided at the
// terminal (agent onboarding design §5.5): typed directly, the address of
// a wallet created there, or mining.payout_address. It is its own file
// rather than part of agent.json — one concern per file — because the
// address is a local mining preference the client owns outright, while
// agent.json mirrors platform state a poll can overwrite. A detached resume
// declares what this file says once enrollment succeeds, without knowing
// how it was decided.
//
// That is why it is not in the state directory (#51). The state directory
// is a writable root of Codex's sandbox, and a first declaration takes
// effect on arrival (payout.go): a payout.json planted there was declared
// by the next resume, the participant's own authority sending the money
// somewhere else. Beside credentials.json, whoever can rewrite this file
// could already rewrite the credential. It is not a defense against a
// sandboxed command that declares with the installation's AS authority
// directly — that is the AS's to answer — only against jevlin declaring
// an address nobody decided.
type PayoutRecord struct {
	dir string
}

// OpenPayoutRecord names payout.json in dir, the directory holding
// credentials.json. Nothing is created until Save.
func OpenPayoutRecord(dir string) (*PayoutRecord, error) {
	if dir == "" {
		return nil, errors.New("auth: payout record directory is empty")
	}
	return &PayoutRecord{dir: dir}, nil
}

func (p *PayoutRecord) store() *Store { return &Store{dir: p.dir} }

// Save writes the address, refusing one that is not a twilight bech32
// address.
func (p *PayoutRecord) Save(address string) error {
	if address == "" {
		return errors.New("auth: refusing to store an empty payout address")
	}
	if err := validatePayoutAddress(address); err != nil {
		return err
	}
	raw, err := json.Marshal(struct {
		Address string `json:"address"`
	}{Address: address})
	if err != nil {
		return fmt.Errorf("auth: encode payout address: %w", err)
	}
	if err := os.MkdirAll(p.dir, 0o700); err != nil { // #nosec G703 -- the jevlin home, beside credentials.json
		return fmt.Errorf("auth: create payout record dir: %w", err)
	}
	return p.store().saveStateFile(payoutRecordFile, raw)
}

// Load returns the stored address, ok=false when none has been decided
// yet. The address is validated on the way back in as well as on the way
// out: a resume declares what it says, and status prints it.
func (p *PayoutRecord) Load() (address string, ok bool, err error) {
	return p.store().loadAddressRecord(payoutRecordFile)
}

// LoadLegacyPayoutAddress reads a payout.json in the state directory, where
// releases before PayoutRecord kept it. Anything there may have been
// written by a sandboxed command, so the address is never declared from
// here; a caller may only compare it with the binding the AS already has
// active.
func (s *Store) LoadLegacyPayoutAddress() (address string, ok bool, err error) {
	return s.loadAddressRecord(payoutRecordFile)
}

// RemoveLegacyPayoutAddress removes the state directory's payout.json by
// name, never what a link there points at. Called once its address is in
// the record beside credentials.json.
func (s *Store) RemoveLegacyPayoutAddress() error {
	if err := os.Remove(filepath.Join(s.dir, payoutRecordFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("auth: remove legacy payout address: %w", err)
	}
	return nil
}

// loadAddressRecord reads one of the {"address": ...} records and refuses an
// address that is not a twilight bech32 address, which also refuses every
// character a terminal would act on: bech32 has none.
func (s *Store) loadAddressRecord(name string) (string, bool, error) {
	raw, err := s.readSecret(name)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var rec struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return "", false, &decodeError{what: name, err: err}
	}
	if err := validatePayoutAddress(rec.Address); err != nil {
		return "", false, fmt.Errorf("auth: %s: %w", name, err)
	}
	return rec.Address, true, nil
}

// ConflictedEpoch is one (slot, epoch) pair another installation of this
// participant is confirmed to hold — WP4b (design aba1245 §2.3/§5.5).
// This installation never obtained a capability for it (ErrEnrollmentConflict
// on join, or ErrProxyBindingMismatch on exchange) and never will: the AS's
// current target moving past TargetEpoch is what proves the observations
// this installation queued for it are worthless, not a clock. That is the
// finding that replaced the first version of this record, which was a
// single CapabilityDeadline-gated slot: the deadline only exists when this
// installation itself once held the epoch before losing it, which is the
// RARE case (ErrProxyBindingMismatch after a prior success) — the common
// case (ErrEnrollmentConflict on join, never held at all) has no deadline
// to compare against and the drop never fired. A single record also meant
// a second conflicted epoch silently clobbered the first's.
type ConflictedEpoch struct {
	SlotID      uint64 `json:"slot_id"`
	TargetEpoch uint64 `json:"target_epoch"`
}

// maxEpochConflicts bounds the conflict set (WP2-adversarial-review
// finding 18): normally each entry is short-lived — RemoveEpochConflicts
// drops it as soon as a flush sees the AS's target move past it — but a
// slot_id the current configuration no longer names can never reach that
// removal path, so without a cap a long-lived installation whose slot_id
// changed repeatedly could accumulate entries forever.
const maxEpochConflicts = 64

// SaveEpochConflict records that ErrEnrollmentConflict or
// ErrProxyBindingMismatch was seen for (slotID, targetEpoch): another
// installation of this participant holds it. Adds to the bounded set; a
// pair already on file is left alone rather than duplicated. Past
// maxEpochConflicts the oldest entry (index 0 — the set is always
// appended to, never reordered) is evicted to make room, on the
// reasoning that a conflict this installation cannot even remember
// having queued observations against is one it has already lost track
// of usefully anyway.
func (s *Store) SaveEpochConflict(slotID, targetEpoch uint64) error {
	set, err := s.loadEpochConflicts()
	if err != nil {
		return err
	}
	for _, c := range set {
		if c.SlotID == slotID && c.TargetEpoch == targetEpoch {
			return nil
		}
	}
	set = append(set, ConflictedEpoch{SlotID: slotID, TargetEpoch: targetEpoch})
	if len(set) > maxEpochConflicts {
		set = set[len(set)-maxEpochConflicts:]
	}
	return s.saveEpochConflicts(set)
}

// EpochConflicts returns every (slot, epoch) pair this installation knows
// another installation of this participant holds. Bounded (maxEpochConflicts):
// an entry leaves the set as soon as a flush observes the AS's current target
// has moved past it (RemoveEpochConflicts), which happens on ordinary epoch
// rollover regardless of whether this installation had anything queued for
// it.
func (s *Store) EpochConflicts() ([]ConflictedEpoch, error) {
	return s.loadEpochConflicts()
}

// PruneEpochConflictsForOtherSlots drops every entry whose SlotID is not
// currentSlotID (WP2-adversarial-review finding 18): a slot_id
// reconfiguration strands whatever conflicts were recorded under the old
// one — the state-based drop in flush.go only ever compares against the
// CURRENT slot's target epoch, so an entry for a different slot can never
// be resolved by ordinary epoch advancement and would sit in the set
// forever without this.
func (s *Store) PruneEpochConflictsForOtherSlots(currentSlotID uint64) error {
	set, err := s.loadEpochConflicts()
	if err != nil {
		return err
	}
	kept := set[:0]
	for _, c := range set {
		if c.SlotID == currentSlotID {
			kept = append(kept, c)
		}
	}
	if len(kept) == len(set) {
		return nil // nothing pruned; skip the write
	}
	return s.saveEpochConflicts(kept)
}

// RemoveEpochConflicts drops the named pairs from the set — called once
// their queued observations have been acted on (dropped, or found already
// gone), never speculatively: a caller that removed a pair before
// processing it and then failed to open the spool would lose the record
// that anything needed doing.
func (s *Store) RemoveEpochConflicts(done []ConflictedEpoch) error {
	if len(done) == 0 {
		return nil
	}
	set, err := s.loadEpochConflicts()
	if err != nil {
		return err
	}
	kept := set[:0]
	for _, c := range set {
		remove := false
		for _, d := range done {
			if c.SlotID == d.SlotID && c.TargetEpoch == d.TargetEpoch {
				remove = true
				break
			}
		}
		if !remove {
			kept = append(kept, c)
		}
	}
	return s.saveEpochConflicts(kept)
}

func (s *Store) saveEpochConflicts(set []ConflictedEpoch) error {
	if set == nil {
		set = []ConflictedEpoch{}
	}
	raw, err := json.Marshal(set)
	if err != nil {
		return fmt.Errorf("auth: encode epoch conflicts: %w", err)
	}
	return s.saveStateFile("epoch_conflicts.json", raw)
}

func (s *Store) loadEpochConflicts() ([]ConflictedEpoch, error) {
	raw, err := s.readSecret("epoch_conflicts.json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var set []ConflictedEpoch
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, &decodeError{what: "epoch conflicts", err: err}
	}
	return set, nil
}

// PayoutBindingHeld is what status shows when connect declined to declare
// a payout address unattended because the AS already has a DIFFERENT
// address active for this participant (agent onboarding design §5.5:
// "declares nothing ... status reports both addresses and that changing
// the binding is an operator-activated change"). Cleared once the two
// addresses agree (a later read-before-declare finds Active == Local, or
// an operator activates the change and a later read reflects it).
type PayoutBindingHeld struct {
	Local  string `json:"local"`
	Active string `json:"active"`
	// HeldFor is one of the AS's known reasons (HeldReplacesActive,
	// HeldAddressInUse — payout.go), or empty when the AS gave none or one
	// this version does not know. A reason is a word this client can say
	// something true about, and this file is in a directory a sandboxed
	// command can rewrite: a reason that is not one of those words is not
	// stored, and on load is not a note this client wrote.
	HeldFor string `json:"held_for,omitempty"`
}

// knownHoldReason reports a hold reason this client can explain.
func knownHoldReason(reason string) bool {
	return reason == HeldReplacesActive || reason == HeldAddressInUse
}

// SavePayoutBindingHeld records that declaration was skipped because the
// AS's active address differs from the one this installation would
// declare.
//
// A hold is recorded whatever the AS said about it: a reason this version
// does not know, or an active address that is not a twilight address, is
// left out of the note rather than keeping the note from being written,
// since the hold is the fact and those are its detail.
func (s *Store) SavePayoutBindingHeld(local, active string, heldFor string) error {
	if !knownHoldReason(heldFor) {
		heldFor = ""
	}
	if active != "" && validatePayoutAddress(active) != nil {
		active = ""
	}
	rec := PayoutBindingHeld{Local: local, Active: active, HeldFor: heldFor}
	if err := checkPayoutBindingHeld(rec); err != nil {
		return err
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("auth: encode payout binding held: %w", err)
	}
	return s.saveStateFile("payout_binding_held.json", raw)
}

// LoadPayoutBindingHeld returns the stored held-binding note, ok=false
// when declaration has never been held (or the hold has been cleared).
// status and doctor print all three of its strings, so a record that
// fails checkPayoutBindingHeld is an error rather than a note.
func (s *Store) LoadPayoutBindingHeld() (rec PayoutBindingHeld, ok bool, err error) {
	raw, err := s.readSecret("payout_binding_held.json")
	if errors.Is(err, fs.ErrNotExist) {
		return PayoutBindingHeld{}, false, nil
	}
	if err != nil {
		return PayoutBindingHeld{}, false, err
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return PayoutBindingHeld{}, false, &decodeError{what: "payout binding held", err: err}
	}
	if err := checkPayoutBindingHeld(rec); err != nil {
		return PayoutBindingHeld{}, false, err
	}
	return rec, true, nil
}

// checkPayoutBindingHeld holds the note to what this client writes into it.
// Both addresses are ones the AS returned or this client declared, so both
// are twilight bech32: the review planted an "active address" of free text
// ("…is revoked. To restore payment run: jevlin payout set <theirs>") and
// status and doctor printed it as the AS's word. The reason is the AS's,
// and its hold vocabulary is open (payout.go: a client prints a reason it
// does not know), so it is held to the shape every reason has, an
// upper-case token, rather than to a list.
func checkPayoutBindingHeld(rec PayoutBindingHeld) error {
	if err := validatePayoutAddress(rec.Local); err != nil {
		return fmt.Errorf("auth: payout binding held: %w", err)
	}
	if rec.Active != "" {
		if err := validatePayoutAddress(rec.Active); err != nil {
			return fmt.Errorf("auth: payout binding held: the active address: %w", err)
		}
	}
	if rec.HeldFor != "" && !knownHoldReason(rec.HeldFor) {
		return errors.New("auth: payout binding held: the reason is not one this client records")
	}
	if field := recordTextProblem(rec); field != "" {
		return fmt.Errorf("auth: payout binding held: its %s holds a control, format or separator character", field)
	}
	return nil
}

// ClearPayoutBindingHeld removes the held-binding note once the addresses
// agree again.
func (s *Store) ClearPayoutBindingHeld() error {
	err := os.Remove(filepath.Join(s.dir, "payout_binding_held.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// SavePayoutDeclared records that address was confirmed ACTIVE for this
// participant — either just declared successfully, or found already
// matching a read-before-declare — so a later poll with nothing new to say
// can skip the AS round trip entirely (WP2-review defect 2's "avoid
// redundant declare calls" question) and so shouldResume (connect.go) can
// tell, from disk alone, whether an on-file address still needs acting on.
// A DIFFERENT address later saved via SavePayoutAddress makes this stale by
// construction — callers compare the two rather than clearing this file.
func (s *Store) SavePayoutDeclared(address string) error {
	if address == "" {
		return errors.New("auth: refusing to record an empty address as declared")
	}
	if err := validatePayoutAddress(address); err != nil {
		return err
	}
	raw, err := json.Marshal(struct {
		Address string `json:"address"`
	}{Address: address})
	if err != nil {
		return fmt.Errorf("auth: encode payout declared: %w", err)
	}
	return s.saveStateFile("payout_declared.json", raw)
}

// LoadPayoutDeclared returns the address last confirmed active, ok=false
// when nothing has ever been declared or confirmed from this installation.
// An address that is not a twilight bech32 address is an error, which
// addressSettled reads as "not settled": the next poll asks the AS again.
func (s *Store) LoadPayoutDeclared() (address string, ok bool, err error) {
	return s.loadAddressRecord("payout_declared.json")
}

// SaveMiningEnabled persists the explicit runtime mining decision. The
// versioned record is the authority after it exists; configuration may seed
// this file during an approved onboarding decision but never overrides it.
func (s *Store) SaveMiningEnabled(enabled bool) error {
	raw, err := json.Marshal(struct {
		Version int  `json:"version"`
		Enabled bool `json:"enabled"`
	}{Version: miningDecisionVersion, Enabled: enabled})
	if err != nil {
		return fmt.Errorf("auth: encode mining decision: %w", err)
	}
	if err := s.saveStateFile("mining_decision.json", raw); err != nil {
		return err
	}
	// Health is diagnostic only. A successful explicit decision write proves
	// this component healthy; failure to clear the diagnostic must not make
	// the participant's decision write fail.
	_ = s.ClearHealth(HealthDecision)
	return nil
}

// LoadMiningEnabled is the compatibility convenience wrapper. Callers that
// need to distinguish undecided from degraded must use ReadMiningDecision.
func (s *Store) LoadMiningEnabled() (enabled bool, ok bool, err error) {
	decision := s.ReadMiningDecision()
	if decision.Err != nil {
		return false, decision.Present, decision.Err
	}
	return decision.Enabled, decision.Present, nil
}

// SaveRevokePending marks that `mining disable` stopped mining locally
// but could not confirm the AS accepted the self-service revocation
// (network down, AS unreachable) — the family may still be live there.
// A later flush or resume retries it and clears this marker on success;
// nothing here ever depends on the marker to decide whether mining is
// active locally — mining_decision.json alone (miningActive) already
// settled that, unconditionally, before this file is ever written.
func (s *Store) SaveRevokePending() error {
	return s.saveStateFile("revoke_pending.json", []byte(`{"pending":true}`))
}

// LoadRevokePending reports whether an AS-side revocation from a past
// `mining disable` is still outstanding.
func (s *Store) LoadRevokePending() (pending bool, err error) {
	_, err = s.readSecret("revoke_pending.json")
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ClearRevokePending removes the marker once the AS confirms the
// revocation, or once a fresh `mining enable` mints a new family that
// supersedes whatever the marker was tracking (mining.go: enabling is
// what makes the old family's fate moot, not this file surviving to be
// retried against the new one).
func (s *Store) ClearRevokePending() error {
	err := os.Remove(filepath.Join(s.dir, "revoke_pending.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
