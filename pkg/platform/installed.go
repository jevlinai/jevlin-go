package platform

import (
	"context"
	"fmt"
	"net/http"
)

// Installed acknowledges successful integration setup to the agents API. The
// upstream contract is POST /v1/agents/installed, 204 on success, bearer key only.
// No GA identifier, search content, machine path or API secret is in the body.
func (c *Client) Installed(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBaseURL+"/v1/agents/installed", nil)
	if err != nil {
		return fmt.Errorf("platform: build installation acknowledgment: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("platform: installation acknowledgment failed: %w", err)
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("platform: installation acknowledgment status %d", resp.StatusCode)
	}
	return nil
}
