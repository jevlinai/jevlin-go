package main

// jevlin limits, read-only: the request's shape, the exact-money
// rendering, the envelope, and the refusals — all against a loopback
// stub, asserted on the bytes sent and the words shown.

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func runLimits(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := cmdLimits(args, &out, &errOut, envOf(env))
	return code, out.String(), errOut.String()
}

func TestLimitsReadsCapsAndLedgerWithOneBearerGet(t *testing.T) {
	var sawMethod, sawPath, sawAuth string
	fr, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, r *http.Request) {
		sawMethod, sawPath, sawAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"daily_micros":5000000,"weekly_micros":0,` +
			`"today":{"spent_micros":123456,"ceiling_micros":5000000,"remaining_micros":4876544}}`))
	})
	code, out, errOut := runLimits(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional"}, "-config", cfg)
	if code != exitOK {
		t.Fatalf("exit %d, stderr=%q", code, errOut)
	}
	if sawMethod != http.MethodGet || sawPath != "/v1/limits" || sawAuth != "Bearer sr-fictional" {
		t.Fatalf("request shape: %s %s auth=%q", sawMethod, sawPath, sawAuth)
	}
	// An explicit 0 is "none" (the wire's spelling of no cap); a field the
	// router omitted is "not reported", never an invented number.
	for _, want := range []string{
		"daily    $5.00",
		"weekly   none",
		"monthly  not reported",
		"spent $0.123456",
		"effective ceiling $5.00",
		"remaining $4.876544",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.reqs) != 1 {
		t.Errorf("%d requests, want exactly 1", len(fr.reqs))
	}
}

func TestLimitsJSONCarriesTheRawMicros(t *testing.T) {
	_, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"daily_micros":5000000,"today":{"spent_micros":0}}`))
	})
	code, out, _ := runLimits(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional"}, "-config", cfg, "-json")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	env := decodeEnvelope(t, out)
	if envField(t, env, "command") != "limits" || envField(t, env, "code") != "ok" || envField(t, env, "action") != actionNone {
		t.Fatalf("header: %v", env)
	}
	data, _ := env["data"].(map[string]any)
	if data == nil || data["daily_micros"] != float64(5_000_000) {
		t.Fatalf("data: %v", env["data"])
	}
	if _, present := data["weekly_micros"]; present {
		t.Errorf("an unreported cap was invented in the envelope: %v", data)
	}
	today, _ := data["today"].(map[string]any)
	if today == nil || today["spent_micros"] != float64(0) {
		t.Fatalf("today: %v", data["today"])
	}
}

func TestLimitsRefusalsClassifyByStatusAndCode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		wantExit   int
		wantCode   string
		wantAction string
	}{
		{"401 is a credential, not a registration", 401, `{"error":"bad key","code":"bad_key"}`, exitClientErr, "bad_key", actionLogin},
		{"500 is retryable", 500, `{"error":"boom","code":"internal"}`, exitServerErr, "internal", actionRetry},
		{"404 is a deployment problem", 404, `{"error":"no route"}`, exitClientErr, "router_rejected", actionReport},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			code, out, _ := runLimits(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional"}, "-config", cfg, "-json")
			if code != tc.wantExit {
				t.Fatalf("exit %d, want %d: %s", code, tc.wantExit, out)
			}
			env := decodeEnvelope(t, out)
			if envField(t, env, "code") != tc.wantCode || envField(t, env, "action") != tc.wantAction {
				t.Fatalf("classification: %v", env)
			}
		})
	}
	t.Run("the 401 names login for a person too", func(t *testing.T) {
		_, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"bad key","code":"bad_key"}`))
		})
		code, _, errOut := runLimits(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional"}, "-config", cfg)
		if code != exitClientErr || !strings.Contains(errOut, "jevlin login") {
			t.Fatalf("exit %d, stderr=%q", code, errOut)
		}
	})
	t.Run("no credential answers connect, before any dial", func(t *testing.T) {
		fr, cfg, _ := newFakeRouter(t, nil)
		code, out, _ := runLimits(t, nil, "-config", cfg, "-json")
		if code != exitClientErr {
			t.Fatalf("exit %d: %s", code, out)
		}
		env := decodeEnvelope(t, out)
		if envField(t, env, "code") != "no_credential" || envField(t, env, "action") != actionConnect {
			t.Fatalf("classification: %v", env)
		}
		fr.mu.Lock()
		defer fr.mu.Unlock()
		if len(fr.reqs) != 0 {
			t.Error("a credential-less limits call dialed the router anyway")
		}
	})
}

// A search's 402 names the one remedy that clears it, branched on the
// router's own code; an unrecognized code keeps the bare status line.
// The raw-router compatibility output and the exit class are untouched
// either way.
func TestASearch402NamesItsRemedy(t *testing.T) {
	for _, tc := range []struct {
		name, code, wantFragment string
	}{
		{"credits exhausted", "credits_exhausted", "out of credit"},
		{"spend ceiling", "spend_ceiling", "jevlin limits"},
		{"budget exceeded", "budget_exceeded", "cheaper -tier"},
		{"unknown code keeps the bare line", "mystery_402", "HTTP 402\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"error":"no","code":"` + tc.code + `"}`
			_, cfg, root := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusPaymentRequired)
				_, _ = w.Write([]byte(body))
			})
			h := fixedSearchOps(root)
			code, out, errOut := runSearch(t, h, map[string]string{"JEVLIN_API_KEY": "sr-fictional"},
				"-config", cfg, "q")
			if code != exitClientErr {
				t.Fatalf("exit %d, want %d", code, exitClientErr)
			}
			if out != body {
				t.Errorf("-format json no longer prints the router's bytes: %q", out)
			}
			if !strings.Contains(errOut, tc.wantFragment) {
				t.Errorf("stderr %q missing %q", errOut, tc.wantFragment)
			}
		})
	}
}

// dollars is exact integer arithmetic: never a float, at least two
// decimals, trailing zeros beyond them trimmed.
func TestDollarsRendersMicrosExactly(t *testing.T) {
	for micros, want := range map[int64]string{
		0:          "$0.00",
		5:          "$0.000005",
		120_000:    "$0.12",
		123_456:    "$0.123456",
		1_000_000:  "$1.00",
		2_500_000:  "$2.50",
		-1_500_000: "-$1.50",
	} {
		if got := dollars(micros); got != want {
			t.Errorf("dollars(%d) = %q, want %q", micros, got, want)
		}
	}
}

// json.Marshal of the decoded report is what the envelope carries; a
// malformed or oversized answer must never become a zeroed ledger.
func TestLimitsRefusesAnUnusableAnswer(t *testing.T) {
	t.Run("not JSON", func(t *testing.T) {
		_, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<html>router lol</html>"))
		})
		code, out, _ := runLimits(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional"}, "-config", cfg, "-json")
		if code != exitServerErr {
			t.Fatalf("exit %d: %s", code, out)
		}
		if env := decodeEnvelope(t, out); envField(t, env, "code") != "invalid_router_response" {
			t.Fatalf("classification: %v", env)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		_, cfg, _ := newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"daily_micros":`))
			_, _ = w.Write(bytes.Repeat([]byte("1"), int(searchMaxBody)+2))
			_, _ = w.Write([]byte(`}`))
		})
		code, out, _ := runLimits(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional"}, "-config", cfg, "-json")
		if code != exitServerErr {
			t.Fatalf("exit %d: %s", code, out)
		}
		if env := decodeEnvelope(t, out); envField(t, env, "code") != "invalid_router_response" {
			t.Fatalf("classification: %v", env)
		}
	})
}
