package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

// cacheAnswer is a router answer whose usage ledger says what the test
// needs it to say about the cache.
func cacheAnswer(t *testing.T, refresh bool) func(map[string]any) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(readSearchTestdata(t, filepath.Join("router", "merged-view-success.merge-v2.json")), &doc); err != nil {
		t.Fatal(err)
	}
	usage, _ := doc["usage"].(map[string]any)
	if usage == nil {
		usage = map[string]any{}
	}
	if refresh {
		usage["cache_refresh"] = true
	}
	doc["usage"] = usage
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return func(map[string]any) []byte { return raw }
}

// sentBytes runs one --stdin search and returns the exact bytes the router
// received.
func sentBytes(t *testing.T, stdin string) []byte {
	t.Helper()
	var fr *fakeRouter
	fr, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/fast-sets" {
			_, _ = w.Write([]byte(realFastSets))
			return
		}
		_, _ = w.Write(cacheAnswer(t, false)(nil))
	})
	code, out, errOut := runSearchStdin(t, fixedSearchOps(root), map[string]string{"JEVLIN_API_KEY": "k"}, stdin, "-config", cfg)
	if code != exitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	_, body := fr.last(t)
	return body
}

func TestACacheRefreshReachesTheRouterAsTheCallerWroteIt(t *testing.T) {
	body := sentBytes(t, `{"version":1,"query":"q","cache":"refresh"}`)
	if !bytes.Contains(body, []byte(`"cache":"refresh"`)) {
		t.Fatalf("the router was not sent cache refresh: %s", body)
	}
}

func TestACacheOfDefaultIsTheSameAsNone(t *testing.T) {
	for _, stdin := range []string{
		`{"version":1,"query":"q","cache":"default"}`,
		`{"version":1,"query":"q"}`,
	} {
		if body := sentBytes(t, stdin); bytes.Contains(body, []byte(`"cache"`)) {
			t.Errorf("%s: the router was sent a cache field: %s", stdin, body)
		}
	}
}

func TestACacheTheRouterWouldRefuseNeverLeaves(t *testing.T) {
	for _, value := range []string{`"bypass"`, `1`, `true`, `null`, `""`, `"Refresh"`, `["refresh"]`, `{}`} {
		stdin := `{"version":1,"query":"q","cache":` + value + `}`
		_, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) { t.Error("the router was called") })
		code, out, _ := runSearchStdin(t, fixedSearchOps(root), map[string]string{"JEVLIN_API_KEY": "k"}, stdin, "-config", cfg)
		env := decodeEnvelope(t, out)
		if code != exitUsage || env["code"] != codeInvalidCache || env["action"] != actionFixInput {
			t.Errorf("%s: exit %d code %v action %v", value, code, env["code"], env["action"])
		}
		msg, _ := env["error"].(map[string]any)["message"].(string)
		if !bytes.Contains([]byte(msg), []byte(`"cache"`)) {
			t.Errorf("%s: the error does not name the field: %q", value, msg)
		}
	}
}

func TestTheFreshFlagSendsCacheRefreshAndNoFlagSendsNone(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"with --fresh", []string{"--fresh"}, true},
		{"with -fresh", []string{"-fresh"}, true},
		{"without it", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(cacheAnswer(t, false)(nil))
			})
			args := append([]string{"-config", cfg}, tc.args...)
			code, out, errOut := runSearch(t, fixedSearchOps(root), map[string]string{"JEVLIN_API_KEY": "k"}, append(args, "q")...)
			if code != exitOK {
				t.Fatalf("exit %d out %q err %q", code, out, errOut)
			}
			_, body := fr.last(t)
			var sent map[string]any
			if err := json.Unmarshal(body, &sent); err != nil {
				t.Fatal(err)
			}
			got, has := sent["cache"]
			if tc.want && got != "refresh" {
				t.Errorf("the router was sent cache %v, want refresh", got)
			}
			if !tc.want && has {
				t.Errorf("the router was sent cache %v with no flag", got)
			}
		})
	}
}

func TestFreshBesideStdinIsRefused(t *testing.T) {
	_, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) { t.Error("the router was called") })
	code, out, _ := runSearchStdin(t, fixedSearchOps(root), map[string]string{"JEVLIN_API_KEY": "k"},
		`{"version":1,"query":"q"}`, "-config", cfg, "--fresh")
	env := decodeEnvelope(t, out)
	if code != exitUsage || env["code"] != codeInvalidFlags || env["action"] != actionFixInput {
		t.Errorf("exit %d code %v action %v", code, env["code"], env["action"])
	}
}

func TestACacheRefreshAnswerIsSaidToBeFresh(t *testing.T) {
	for _, refresh := range []bool{true, false} {
		_, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(cacheAnswer(t, refresh)(nil))
		})
		code, out, errOut := runSearchStdin(t, fixedSearchOps(root), map[string]string{"JEVLIN_API_KEY": "k"},
			`{"version":1,"query":"q","cache":"refresh"}`, "-config", cfg)
		if code != exitOK {
			t.Fatalf("exit %d out %q err %q", code, out, errOut)
		}
		usage, _ := decodeEnvelope(t, out)["result"].(map[string]any)["usage"].(map[string]any)
		got, has := usage["cache_refresh"]
		if refresh && got != true {
			t.Errorf("usage.cache_refresh = %v, want true", got)
		}
		if !refresh && has {
			t.Errorf("usage.cache_refresh = %v on an answer that did not say it", got)
		}
	}
}
