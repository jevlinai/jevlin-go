package main

import (
	"strings"
	"testing"
)

// Uninstall says Codex's record of the participant's approval of jevlin's
// hooks stays only when there is one, read the way status reads it. With
// no [hooks.state] table for these hooks there is no note; with one there
// is; when the record cannot be told (jevlin's handler shares a group)
// the note says so rather than claim a record.
func TestCodexUninstallMentionsTheApprovalRecordOnlyWhenThereIsOne(t *testing.T) {
	for _, c := range []struct {
		name   string
		config string
		shared bool
		want   string // a substring of the note, or "" for no note
	}{
		{"no record", "model = \"gpt-5\"\n", false, ""},
		{"no config at all", "", false, ""},
		{"a record for every hook", "model = \"gpt-5\"\n", false, "Codex's record of your approval of these hooks stays in"},
		{"a record for one hook", "model = \"gpt-5\"\n", false, "Codex's record of your approval of these hooks stays in"},
		{"cannot be told", "model = \"gpt-5\"\n", true, "could not be told"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, ops, paths, entry := codexStatusMachine(t)
			switch c.name {
			case "no config at all":
				delete(m.files, paths.codexConfig)
			case "a record for every hook":
				m.files[paths.codexConfig] = []byte(c.config + allFive(paths))
			case "a record for one hook":
				m.files[paths.codexConfig] = []byte(c.config + approvalTables(paths.codexHooks, "0", "stop"))
			default:
				m.files[paths.codexConfig] = []byte(c.config)
			}
			if c.shared {
				putInOurStopGroup(t, m, paths.codexHooks, false)
			}
			var p agentPlan
			codexTarget{}.PlanUninstall(ops, paths, entry, envOf(map[string]string{"CODEX_HOME": "/Users/u/.codex"}), &p)
			var got []string
			for _, n := range p.notes {
				if strings.Contains(n, "approval of these hooks") {
					got = append(got, n)
				}
			}
			switch {
			case c.want == "" && len(got) != 0:
				t.Errorf("a note about an approval record that does not exist:\n%s", strings.Join(got, "\n"))
			case c.want != "" && (len(got) != 1 || !strings.Contains(got[0], c.want)):
				t.Errorf("notes = %q, want one containing %q", got, c.want)
			}
		})
	}
}
