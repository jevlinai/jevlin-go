package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A register response whose agent_id the record would refuse is refused
// before connect journals it, so nothing is published that a later run
// cannot finish: the review's stub answered agent_id "agent-1<RLM>", connect
// wrote the journal and the key, then failed to save agent.json, and every
// run after it failed the same way.
func TestARegisterResponseTheRecordWouldRefuseIsNeverJournaled(t *testing.T) {
	withShortConnectTimings(t)
	platform := newStubPlatform(t)
	platform.registerAgentID = "agent-1\u200f"
	cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
	cfg := mustLoadConfig(t, cfgPath)

	code, _, errOut := runConnect(t, cfgPath, nil)
	if code == exitOK {
		t.Fatalf("connect accepted a register response with a bidi mark in agent_id:\n%s", errOut)
	}
	for _, p := range []string{testJournalPath(t, cfgPath), credentialsPath(cfg.Miner), filepath.Join(stateDir, "agent.json")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s was written for a response connect refused: %v", p, err)
		}
	}
	if code, _, errOut := runConnect(t, cfgPath, nil); code == exitOK || registers(platform) != 2 {
		t.Fatalf("the next connect did not simply try again (exit %d, %d registers):\n%s", code, registers(platform), errOut)
	}
}

func registers(p *stubPlatform) int {
	n, _, _ := p.counts()
	return n
}
