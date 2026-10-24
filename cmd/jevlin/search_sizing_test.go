package main

// merged_limit and max_chars let a caller size the router's merged view:
// more pages when a list question's first pass looks thin, fewer characters
// when the text is more than it needs. Each reaches the router as the
// caller's number, and a value the router would refuse never leaves.

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheMergedViewsSizeReachesTheRouter(t *testing.T) {
	raw := readSearchTestdata(t, filepath.Join("router", "merged-view-success.merge-v2.json"))
	for _, tc := range []struct {
		name, stdin         string
		wantLimit, wantMaxC any
	}{
		{"both", `{"version":1,"query":"q","merged_limit":20,"max_chars":6000}`, float64(20), float64(6000)},
		{"the bounds", `{"version":1,"query":"q","merged_limit":50,"max_chars":1}`, float64(50), float64(1)},
		{"the lower bound", `{"version":1,"query":"q","merged_limit":1}`, float64(1), nil},
		{"neither", `{"version":1,"query":"q"}`, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, sent, _ := searchServing(t, raw, tc.stdin)
			if sent["view"] != "merged" {
				t.Errorf("view %v: the router takes these only beside view merged", sent["view"])
			}
			for key, want := range map[string]any{"merged_limit": tc.wantLimit, "max_chars": tc.wantMaxC} {
				got, has := sent[key]
				if want == nil {
					if has {
						t.Errorf("the router was sent %s %v, which the caller did not ask for", key, got)
					}
					continue
				}
				if got != want {
					t.Errorf("the router was sent %s %v, want %v", key, got, want)
				}
			}
		})
	}
}

func TestAnOutOfRangeSizeNeverReachesTheRouter(t *testing.T) {
	for _, tc := range []struct {
		name, stdin, code, field string
	}{
		{"merged_limit 0", `{"version":1,"query":"q","merged_limit":0}`, codeInvalidMergedLimit, "merged_limit"},
		{"merged_limit 51", `{"version":1,"query":"q","merged_limit":51}`, codeInvalidMergedLimit, "merged_limit"},
		{"merged_limit negative", `{"version":1,"query":"q","merged_limit":-3}`, codeInvalidMergedLimit, "merged_limit"},
		{"merged_limit fraction", `{"version":1,"query":"q","merged_limit":12.5}`, codeInvalidMergedLimit, "merged_limit"},
		{"merged_limit string", `{"version":1,"query":"q","merged_limit":"12"}`, codeInvalidMergedLimit, "merged_limit"},
		{"merged_limit null", `{"version":1,"query":"q","merged_limit":null}`, codeInvalidMergedLimit, "merged_limit"},
		{"max_chars 0", `{"version":1,"query":"q","max_chars":0}`, codeInvalidMaxChars, "max_chars"},
		{"max_chars negative", `{"version":1,"query":"q","max_chars":-100}`, codeInvalidMaxChars, "max_chars"},
		{"max_chars fraction", `{"version":1,"query":"q","max_chars":100.5}`, codeInvalidMaxChars, "max_chars"},
		{"max_chars string", `{"version":1,"query":"q","max_chars":"4000"}`, codeInvalidMaxChars, "max_chars"},
		{"max_chars null", `{"version":1,"query":"q","max_chars":null}`, codeInvalidMaxChars, "max_chars"},
		{"max_chars past int64", `{"version":1,"query":"q","max_chars":99999999999999999999}`, codeInvalidMaxChars, "max_chars"},
		// The router's other merged-view option is not this client's to expose.
		{"excerpt_mode", `{"version":1,"query":"q","excerpt_mode":"full"}`, codeUnknownField, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(routerBody))
			})
			code, out, errOut := runSearchStdin(t, fixedSearchOps(root), map[string]string{"JEVLIN_API_KEY": "k"}, tc.stdin, "-config", cfg)
			if code != exitUsage {
				t.Fatalf("exit %d, want %d (%s)", code, exitUsage, out)
			}
			env := decodeEnvelope(t, out)
			if got := envField(t, env, "code"); got != tc.code {
				t.Errorf("code %q, want %q", got, tc.code)
			}
			if got := envField(t, env, "action"); got != actionFixInput {
				t.Errorf("action %q, want %q", got, actionFixInput)
			}
			if msg, _ := objOf(env["error"])["message"].(string); !strings.Contains(msg, tc.field) {
				t.Errorf("the message %q does not name %q", msg, tc.field)
			}
			if errOut != "" {
				t.Errorf("machine mode wrote on stderr: %q", errOut)
			}
			fr.mu.Lock()
			defer fr.mu.Unlock()
			if len(fr.reqs) != 0 {
				t.Errorf("a refused request reached the router %d time(s)", len(fr.reqs))
			}
		})
	}
}
