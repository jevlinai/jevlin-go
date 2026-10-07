package auth

import (
	"strings"
	"testing"
)

func TestJudgeStateAccess(t *testing.T) {
	const user = "S-1-5-21-1-2-3-1001"
	const users = "S-1-5-32-545"
	id := func(s string) string { return s }
	cases := []struct {
		name string
		acc  stateAccess
		want string // "" = accepted
	}{
		{"owner only", stateAccess{owner: user, aces: []stateACE{{allow: true, sid: user}}}, ""},
		{"profile default", stateAccess{owner: user, aces: []stateACE{
			{allow: true, sid: sidLocalSystem}, {allow: true, sid: sidAdministrators}, {allow: true, sid: user},
		}}, ""},
		{"elevated owner", stateAccess{owner: sidAdministrators, aces: []stateACE{{allow: true, sid: user}}}, ""},
		{"inherited Users", stateAccess{owner: user, aces: []stateACE{
			{allow: true, sid: user}, {allow: true, sid: users},
		}}, "grants access to " + users},
		{"deny entry ignored", stateAccess{owner: user, aces: []stateACE{
			{allow: false, sid: users}, {allow: true, sid: user},
		}}, ""},
		{"foreign owner", stateAccess{owner: "S-1-5-21-1-2-3-1002", aces: []stateACE{{allow: true, sid: user}}}, "is owned by S-1-5-21-1-2-3-1002"},
		{"null DACL", stateAccess{owner: user, nullDACL: true}, "everyone can open it"},
	}
	for _, c := range cases {
		err := judgeStateAccess(c.acc, user, id)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}
