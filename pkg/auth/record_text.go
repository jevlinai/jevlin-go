package auth

import (
	"reflect"
	"strings"

	"github.com/jevlinai/jevlin-go/internal/termtext"
)

// The state directory is a writable root of Codex's sandbox (hard invariant
// 19), so a record read back from it is only what the last writer left
// there. status, doctor, connect and mining enable print these records'
// strings to the participant's terminal; a control, C1 or bidi character in
// one is a terminal escape or a reordered line in jevlin's own output, with
// jevlin's authority behind it. The platform client refuses those
// characters in every field of what it receives that reaches a record
// (pkg/platform: the agent id, the claim link, code and times, the scopes,
// the slots and the last enrollment), and the save side refuses what the
// load side would, so a record this client wrote never holds one, and a
// record that does was not written by it.

// recordTextProblem names the first string in rec, a struct or a pointer to
// one, that holds a character termtext refuses: its JSON name, or "" when
// there is none. It walks every exported string, string slice and nested
// struct by reflection rather than a list of fields, so a string field added
// to a record later is checked without anyone remembering to add it.
func recordTextProblem(rec any) string {
	return textProblemIn(reflect.ValueOf(rec), "")
}

func textProblemIn(v reflect.Value, name string) string {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return ""
		}
		return textProblemIn(v.Elem(), name)
	case reflect.String:
		if termtext.HasControlChar(v.String()) {
			return name
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if p := textProblemIn(v.Index(i), name); p != "" {
				return p
			}
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			if p := textProblemIn(v.Field(i), jsonName(f)); p != "" {
				return p
			}
		}
	}
	return ""
}

func jsonName(f reflect.StructField) string {
	if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag != "" && tag != "-" {
		return tag
	}
	return f.Name
}
