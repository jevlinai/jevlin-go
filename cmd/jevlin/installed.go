package main

import (
	"context"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/platform"
)

// Acknowledge only a completed integration write. A refused, dry-run, aborted,
// safety-only or partial plan is not an installed agent. The acknowledgment
// goes to the configured agents API, never Google, and has no event payload.
func installedPlan(p *agentPlan, failures int) bool {
	if failures != 0 || p.aborted != "" || p.unanswerable || len(p.refused) > 0 {
		return false
	}
	for _, w := range p.writes {
		if !w.safety {
			return true
		}
	}
	return false
}
func acknowledgeInstalled(cfgPath string, getenv func(string) string) {
	cfg, _, err := loadConfig(cfgPath, getenv)
	if err != nil {
		return
	}
	key, err := platformKey(cfg, getenv)
	if err != nil || key == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	// A best-effort product acknowledgment must never change setup's result.
	_ = platform.New(cfg.Platform.AgentsAPIURL, cfg.Platform.BaseURL).Installed(ctx, key)
}
