package winacl

import (
	"reflect"
	"strings"
	"testing"
)

// The parser of a descriptor's string form runs on every OS: it is the part of
// reading an access list that needs no Windows API, and the part a wrong
// split would turn into the wrong principal being named.
func TestSDDLTrusteesSplitsEntriesAtTheirClosingParenthesis(t *testing.T) {
	const user = "S-1-5-21-1-2-3-1001"
	cases := []struct {
		name string
		sddl string
		want []string
	}{
		{"two entries, as the owner-only list is written",
			"D:PAI(A;;FA;;;LA)(A;OICIIO;GA;;;LA)", []string{"LA", "LA"}},
		{"with an owner and a group before the list",
			"O:" + user + "G:SYD:PAI(A;OICI;FA;;;" + user + ")(A;;0x1200a9;;;BU)", []string{user, "BU"}},
		{"a conditional entry carries parentheses of its own",
			`D:(XA;;FX;;;S-1-1-0;(@User.Title=="PM" && (@User.Division=="Fin" || @User.Division=="Eng")))(A;;FA;;;SY)`,
			[]string{"S-1-1-0", "SY"}},
		{"a deny entry is still an entry", "D:(D;;FA;;;BG)(A;;FA;;;SY)", []string{"BG", "SY"}},
		{"a SACL after the list is not read", "D:(A;;FA;;;SY)S:(AU;SA;FA;;;WD)", []string{"SY"}},
		{"an empty list", "D:P", nil},
	}
	for _, c := range cases {
		got, err := SDDLTrustees(c.sddl)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: trustees = %q, want %q", c.name, got, c.want)
		}
	}
}

// A string it cannot read is an error, never a shorter list: the caller pairs
// the trustees with binary entries by index, and a missing one would pair every
// later entry with the wrong principal.
func TestSDDLTrusteesRefusesWhatItCannotReadExactly(t *testing.T) {
	cases := []struct{ name, sddl, want string }{
		{"no list", "O:BAG:SY", "no DACL"},
		{"a closing parenthesis too many", "D:(A;;FA;;;SY))", "unbalanced"},
		{"an entry never closed", "D:(A;;FA;;;SY", "unterminated"},
		{"an entry with too few fields", "D:(A;;FA)", "unexpected access entry"},
	}
	for _, c := range cases {
		got, err := SDDLTrustees(c.sddl)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: trustees = %q, err = %v, want an error naming %q", c.name, got, err, c.want)
		}
	}
}

func TestIsAllowACEType(t *testing.T) {
	cases := []struct {
		name string
		typ  uint8
		want bool
	}{
		{"access allowed", 0x0, true},
		{"access allowed, object", 0x5, true},
		{"access allowed, callback", 0x9, true},
		{"access allowed, callback object", 0xB, true},
		{"access denied", 0x1, false},
		{"access denied, object", 0x6, false},
		{"access denied, callback", 0xA, false},
		{"access denied, callback object", 0xC, false},
		{"system audit", 0x2, false},
		{"system alarm", 0x3, false},
		{"mandatory label", 0x11, false},
	}
	for _, c := range cases {
		if got := IsAllowACEType(c.typ); got != c.want {
			t.Errorf("%s (%#x): allow = %v, want %v", c.name, c.typ, got, c.want)
		}
	}
}
