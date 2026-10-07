package main

import (
	"strings"
	"testing"
)

const codexNetConfig = "/home/u/.codex/config.toml"

// The block install writes by default leaves Codex's sandbox network off:
// network_access is not scoped to the search, it opens egress for every
// command Codex runs in its sandbox, in every project.
func TestCodexInstallLeavesTheSandboxNetworkOffByDefault(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	m.files[codexNetConfig] = []byte("model = \"gpt-5\"\n")

	code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes")
	if code != exitOK {
		t.Fatalf("install: %d\n%s%s", code, out, errOut)
	}
	got := string(m.files[codexNetConfig])
	if strings.Contains(got, "network_access = true") || !strings.Contains(got, "network_access = false") {
		t.Errorf("the default block does not leave the network off:\n%s", got)
	}
	if strings.Contains(out, "network_access is on") {
		t.Errorf("the default plan claims the network is on:\n%s", out)
	}
}

// -codex-network on is the opt-in, the plan says what it opens, and a later
// install without the flag keeps it until -codex-network off.
func TestCodexNetworkOptInIsDisclosedKeptAndRevocable(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	m.files[codexNetConfig] = []byte("model = \"gpt-5\"\n")

	code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-codex-network", "on", "-yes")
	if code != exitOK {
		t.Fatalf("install -codex-network on: %d\n%s%s", code, out, errOut)
	}
	got := string(m.files[codexNetConfig])
	if !strings.Contains(got, "network_access = true") || !strings.Contains(got, codexNetworkOptIn) {
		t.Fatalf("the opt-in was not written:\n%s", got)
	}
	for _, want := range []string{"in every project", "can reach any host without asking", "credentials.json", "-codex-network off"} {
		if !strings.Contains(out, want) {
			t.Errorf("the opt-in plan does not say %q:\n%s", want, out)
		}
	}

	// Kept by an install that does not name the flag, and named by status.
	code, out, _ = runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes")
	if code != exitOK || string(m.files[codexNetConfig]) != got {
		t.Fatalf("a plain install did not keep the opt-in (exit %d):\n%s\n%s", code, out, m.files[codexNetConfig])
	}
	_, status, _ := runAgents(t, ops, nil, "status", "-config", cfgPath)
	if !strings.Contains(status, "network_access is on") {
		t.Errorf("status does not name the opt-in:\n%s", status)
	}
	if strings.Contains(status, staleSentence) {
		t.Errorf("status calls an opted-in block stale:\n%s", status)
	}

	code, out, _ = runAgents(t, ops, nil, "install", "-config", cfgPath, "-codex-network", "off", "-yes")
	if code != exitOK {
		t.Fatalf("install -codex-network off: %d\n%s", code, out)
	}
	off := string(m.files[codexNetConfig])
	if strings.Contains(off, "network_access = true") || strings.Contains(off, codexNetworkOptIn) {
		t.Errorf("-codex-network off left the network on:\n%s", off)
	}
}

// A block from before the opt-in existed turned the network on without
// asking. It carries no opt-in, so the next install turns it off and says so,
// and status names it until then.
func TestAnEarlierBlocksNetworkIsTurnedOff(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	m, ops := newFakeMachine("codex")
	m.files[codexNetConfig] = []byte("model = \"gpt-5\"\n")
	if code, out, _ := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
		t.Fatalf("install: %d\n%s", code, out)
	}
	earlier := strings.Replace(string(m.files[codexNetConfig]), "network_access = false", "network_access = true", 1)
	m.files[codexNetConfig] = []byte(earlier)

	_, status, _ := runAgents(t, ops, nil, "status", "-config", cfgPath)
	if !strings.Contains(status, "network_access is on") {
		t.Errorf("status does not flag the earlier block:\n%s", status)
	}

	code, out, _ := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes")
	if code != exitOK {
		t.Fatalf("reinstall: %d\n%s", code, out)
	}
	got := string(m.files[codexNetConfig])
	if strings.Contains(got, "network_access = true") {
		t.Errorf("the earlier block's network is still on:\n%s", got)
	}
	if !strings.Contains(out, "turning network_access off") {
		t.Errorf("the plan does not say it turns the network off:\n%s", out)
	}
}

func TestCodexNetworkFlagIsCheckedAndInstallOnly(t *testing.T) {
	cfgPath, _ := sandboxTestConfig(t)
	_, ops := newFakeMachine("codex")
	if code, _, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-codex-network", "yes", "-dry-run"); code != exitUsage || !strings.Contains(errOut, "on or off") {
		t.Errorf("-codex-network yes: exit %d, %s", code, errOut)
	}
	if code, _, errOut := runAgents(t, ops, nil, "uninstall", "-config", cfgPath, "-codex-network", "on", "-dry-run"); code != exitUsage || !strings.Contains(errOut, "install only") {
		t.Errorf("uninstall -codex-network on: exit %d, %s", code, errOut)
	}
}
