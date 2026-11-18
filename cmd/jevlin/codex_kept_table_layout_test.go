package main

import (
	"strings"
	"testing"
)

// A table Codex wrote inside jevlin's region (codex features enable
// memories, say) is kept where the region was. It is separated from the
// participant's next table by one blank line, as Codex lays out its own
// tables, rather than running straight into its header; and an uninstall
// with no such table stays byte-identical to the file before install.
func TestUninstallSeparatesAKeptTableFromTheNextByABlankLine(t *testing.T) {
	before := "model = \"gpt-5\"\n\n[mcp_servers.foo]\ncommand = \"/bin/echo\"\n"
	for _, c := range []struct {
		name  string
		codex string // what Codex wrote inside the region, if anything
		want  string
	}{
		{"nothing of Codex's inside the region", "", before},
		{"a table of Codex's inside the region", "[features]\nmemories = true\n", "model = \"gpt-5\"\n\n[features]\nmemories = true\n\n[mcp_servers.foo]\ncommand = \"/bin/echo\"\n"},
		{"two tables of Codex's inside the region", "[features]\nmemories = true\n\n[mcp_servers.bar]\ncommand = \"/bin/echo\"\n", "model = \"gpt-5\"\n\n[features]\nmemories = true\n\n[mcp_servers.bar]\ncommand = \"/bin/echo\"\n\n[mcp_servers.foo]\ncommand = \"/bin/echo\"\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfgPath, _ := sandboxTestConfig(t)
			m, ops := newFakeMachine("codex")
			const config = "/home/u/.codex/config.toml"
			m.files[config] = []byte(before)
			if code, out, errOut := runAgents(t, ops, nil, "install", "-config", cfgPath, "-yes"); code != exitOK {
				t.Fatalf("install: %d\n%s%s", code, out, errOut)
			}
			if c.codex != "" {
				installed := string(m.files[config])
				i := strings.LastIndex(installed, agentsMarkerEnd)
				if i < 0 {
					t.Fatalf("no end marker in the installed config:\n%s", installed)
				}
				m.files[config] = []byte(installed[:i] + c.codex + installed[i:])
			}
			code, out, errOut := runAgents(t, ops, nil, "uninstall", "-config", cfgPath, "-yes")
			if code != exitOK {
				t.Fatalf("uninstall: %d\n%s%s", code, out, errOut)
			}
			if got := string(m.files[config]); got != c.want {
				t.Errorf("config.toml after uninstall:\n%s\n--- want ---\n%s\n--- plan ---\n%s", got, c.want, out)
			}
		})
	}
}
