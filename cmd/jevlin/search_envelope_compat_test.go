package main

// "view":"full" is the shape every integration that reads candidates asked
// for, and it may only grow. testdata/search/full-a0ff6ca holds the whole
// --stdin envelope a0ff6ca (v0.4.3) printed for each router fixture under
// "view":"full", captured before the default view changed; the test takes
// today's envelope, removes exactly the keys added since, and requires every
// remaining byte to be the capture's. A field renamed, retyped, reordered or
// dropped fails here; a key added without being named in fullViewAdditions
// fails here too, because the capture never had it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// fullViewAdditions is every key "view":"full" has gained since the
// capture, by its path in the envelope ("[]" stands for each element of a
// list). Nothing else may differ.
var fullViewAdditions = map[string]bool{
	"result.merged.[].published": true,
	"result.merged.[].result_id": true,
	"result.indexes":             true,
	"result.usage.deadline_hit":  true,
	"result.merged.[].source_id": true,
	"result.filters_applied":     true,
	"result.decision.build":      true,
	// 0.7: the trajectory fact beside the mining state, and the router's
	// price band on the usage block.
	"mining.trajectory":           true,
	"result.usage.billing":        true,
	"result.usage.billing_reason": true,
}

// fullCaptures pairs each capture with the router answer it was made from.
var fullCaptures = map[string]string{
	"merged-view-success.main.envelope.json":         "router/merged-view-success.main.json",
	"merged-view-success.merge-v2.envelope.json":     "router/merged-view-success.merge-v2.json",
	"merged-view-two-forms.derived.envelope.json":    "router/merged-view-two-forms.derived.json",
	"merged-view-partial-failure.envelope.json":      "router/merged-view-partial-failure.json",
	"merged-view-no-usable.envelope.json":            "router/merged-view-no-usable.json",
	"balanced_response.envelope.json":                "balanced_response.json",
	"beta-balanced-filters-2026-10-23.envelope.json": "router/beta-balanced-filters-2026-10-23.json",
}

// searchStdoutServing runs one --stdin search against a fake router that
// answers with raw and returns stdout as printed: the bytes, not a decoding
// of them, because the claim is about the bytes.
func searchStdoutServing(t *testing.T, raw []byte, stdin string) string {
	t.Helper()
	_, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	})
	// With a trajectory, as the captures were made with intake recorded:
	// since 0.7 a search without one spools nothing and says so, which is
	// an addition to the mining block, not to the view.
	code, out, errOut := runSearchStdin(t, fixedSearchOps(root), map[string]string{"JEVLIN_API_KEY": "k", bridgeEnv: traceWithHistoryAs("cli")}, stdin, "-config", cfg)
	if code != exitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	return out
}

// jsonWithout re-emits a compact JSON value with the keys at the named
// paths left out and every other byte as it was: keys keep their order and
// scalars their spelling, which a decode into a map and back would not.
func jsonWithout(raw []byte, drop map[string]bool) ([]byte, error) {
	var b bytes.Buffer
	if err := writeWithout(&b, raw, "", drop); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeWithout(b *bytes.Buffer, raw []byte, path string, drop map[string]bool) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || (raw[0] != '{' && raw[0] != '[') {
		b.Write(raw)
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	open, err := dec.Token()
	if err != nil {
		return err
	}
	if open == json.Delim('[') {
		b.WriteByte('[')
		for i := 0; dec.More(); i++ {
			var elem json.RawMessage
			if err := dec.Decode(&elem); err != nil {
				return err
			}
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeWithout(b, elem, path+".[]", drop); err != nil {
				return err
			}
		}
		b.WriteByte(']')
		return nil
	}
	b.WriteByte('{')
	first := true
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("object key %v at %q", keyTok, path)
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return err
		}
		child := key
		if path != "" {
			child = path + "." + key
		}
		if drop[child] {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		k, err := json.Marshal(key)
		if err != nil {
			return err
		}
		b.Write(k)
		b.WriteByte(':')
		if err := writeWithout(b, val, child, drop); err != nil {
			return err
		}
	}
	b.WriteByte('}')
	return nil
}

func TestTheFullViewOnlyAddsToWhatV043Sent(t *testing.T) {
	if len(fullCaptures) == 0 {
		t.Fatal("no captures listed")
	}
	for capture, fixture := range fullCaptures {
		t.Run(capture, func(t *testing.T) {
			want := readSearchTestdata(t, filepath.Join("full-a0ff6ca", capture))
			// The helper must be the identity on what it does not drop, or a
			// match below would prove nothing about the bytes.
			same, err := jsonWithout(bytes.TrimSuffix(want, []byte("\n")), nil)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(append(same, '\n'), want) {
				t.Fatalf("jsonWithout changed the capture with nothing to drop:\n%s\n%s", same, want)
			}

			out := searchStdoutServing(t, readSearchTestdata(t, fixture), `{"version":1,"query":"q","view":"full"}`)
			got, err := jsonWithout([]byte(strings.TrimSuffix(out, "\n")), fullViewAdditions)
			if err != nil {
				t.Fatal(err)
			}
			if string(got)+"\n" != string(want) {
				t.Errorf("\"view\":\"full\" is no longer v0.4.3's plus the named additions.\n--- now, additions removed ---\n%s\n--- a0ff6ca ---\n%s", got, want)
			}
		})
	}
}
