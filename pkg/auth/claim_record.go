package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

const claimRecordFile = "claim.json"

// ClaimBootstrap is the claim link and code the platform issued for one
// agent, with the agent it belongs to.
type ClaimBootstrap struct {
	AgentID        string `json:"agent_id"`
	ClaimURL       string `json:"claim_url"`
	ClaimCode      string `json:"claim_code,omitempty"`
	ClaimExpiresAt string `json:"claim_expires_at,omitempty"`
}

// ClaimRecord holds claim.json beside credentials.json. The claim link is
// the one thing this client tells a person to open, and agent.json is in
// the state directory a sandboxed command can rewrite: a planted record that
// kept this installation's agent id but carried another agent's link, on
// the platform's own origin, was printed by connect, status and mining
// enable, and claiming it put the attacker's agent in the participant's
// account. So the link lives where only an unsandboxed run writes it: the
// publication of a registration, the rebuild from /v1/agents/me and the
// re-mint, each a foreground run outside the sandbox. It is bound to its
// agent id, and nothing prints it for any other agent.
type ClaimRecord struct {
	dir string
}

// OpenClaimRecord names claim.json in dir, the directory holding
// credentials.json. Nothing is created until Save.
func OpenClaimRecord(dir string) (*ClaimRecord, error) {
	if dir == "" {
		return nil, errors.New("auth: claim record directory is empty")
	}
	return &ClaimRecord{dir: dir}, nil
}

func (c *ClaimRecord) store() *Store { return &Store{dir: c.dir} }

func checkClaimBootstrap(b ClaimBootstrap) error {
	if err := ValidAgentID(b.AgentID); err != nil {
		return err
	}
	if b.ClaimURL == "" {
		return errors.New("auth: claim record has no claim_url")
	}
	if field := recordTextProblem(b); field != "" {
		return fmt.Errorf("auth: claim record's %s holds a control, format or separator character", field)
	}
	return nil
}

// Save writes b, refusing a record Load would refuse.
func (c *ClaimRecord) Save(b ClaimBootstrap) error {
	if err := checkClaimBootstrap(b); err != nil {
		return fmt.Errorf("auth: refusing to store a claim record: %w", err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("auth: encode claim record: %w", err)
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil { // #nosec G703 -- the jevlin home, beside credentials.json
		return fmt.Errorf("auth: create claim record dir: %w", err)
	}
	return c.store().saveStateFile(claimRecordFile, raw)
}

// Load returns the stored claim, ok=false when there is none.
func (c *ClaimRecord) Load() (ClaimBootstrap, bool, error) {
	raw, err := c.store().readSecret(claimRecordFile)
	if errors.Is(err, fs.ErrNotExist) {
		return ClaimBootstrap{}, false, nil
	}
	if err != nil {
		return ClaimBootstrap{}, false, err
	}
	var b ClaimBootstrap
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return ClaimBootstrap{}, false, fmt.Errorf("auth: decode claim record: %w", err)
	}
	if err := checkClaimBootstrap(b); err != nil {
		return ClaimBootstrap{}, false, err
	}
	return b, true, nil
}

// For returns the stored claim only when it was issued for agentID.
func (c *ClaimRecord) For(agentID string) (ClaimBootstrap, bool, error) {
	b, ok, err := c.Load()
	if err != nil || !ok || b.AgentID != agentID {
		return ClaimBootstrap{}, false, err
	}
	return b, true, nil
}
