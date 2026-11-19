package main

// -idempotency-key: the caller's key goes on the wire as the Idempotency-Key
// header exactly as given, a key the router would refuse never leaves, no
// key is ever made up, and the router's two answers about a key map to what
// an agent should do.

import (
	"strings"
	"testing"
)

// A valid key is sent as given, on the one POST; without the flag no header
// is sent at all.
func TestTheIdempotencyKeyIsSentAsGiven(t *testing.T) {
	for _, key := range []string{"0b7c0a52-attempt-1", "!", "~", strings.Repeat("k", idempotencyKeyMax), `a"b\c{}`} {
		fr, cfg, _ := newFakeRouter(t, okHandler(decideFixture(t, "decisions-success.json")))
		code, out, _ := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, decideMinimal,
			"--stdin", "-idempotency-key", key, "-config", cfg)
		if code != exitOK {
			t.Fatalf("key %q: exit %d: %s", key, code, out)
		}
		req, _ := fr.last(t)
		if got := req.Header.Values("Idempotency-Key"); len(got) != 1 || got[0] != key {
			t.Errorf("Idempotency-Key %q, want exactly %q", got, key)
		}
		if len(fr.reqs) != 1 {
			t.Errorf("%d requests", len(fr.reqs))
		}
	}
	fr, cfg, _ := newFakeRouter(t, okHandler(decideFixture(t, "decisions-success.json")))
	if code, out, _ := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, decideMinimal, "--stdin", "-config", cfg); code != exitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	if req, _ := fr.last(t); len(req.Header.Values("Idempotency-Key")) != 0 {
		t.Errorf("a decide without the flag sent Idempotency-Key %q", req.Header.Values("Idempotency-Key"))
	}
}

// Every key the router's rule refuses (validIdempotencyKey in search-router's
// internal/decisions/idempotency.go) is refused here, before stdin is read
// or anything is sent: each byte outside 0x21 to 0x7e, and each length
// outside 1 to 255.
func TestAKeyTheRouterWouldRefuseNeverLeaves(t *testing.T) {
	var keys []string
	for b := 0; b < 256; b++ {
		if b < 0x21 || b > 0x7e {
			keys = append(keys, "k"+string([]byte{byte(b)})+"k")
		}
	}
	keys = append(keys, "", strings.Repeat("k", idempotencyKeyMax+1), "é", "k k")
	for _, key := range keys {
		fr, cfg, _ := newFakeRouter(t, okHandler(decideFixture(t, "decisions-success.json")))
		code, out, errOut := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, "not even a request",
			"--stdin", "-idempotency-key", key, "-config", cfg)
		env := decodeEnvelope(t, out)
		if code != exitUsage || env["code"] != codeInvalidIdempotencyKey || env["action"] != actionFixInput || errOut != "" {
			t.Errorf("key %q: exit %d, %v / %v, stderr %q; want %d, %s / fix_input", key, code, env["code"], env["action"], errOut, exitUsage, codeInvalidIdempotencyKey)
		}
		if len(fr.reqs) != 0 {
			t.Errorf("key %q: the router received %d requests", key, len(fr.reqs))
		}
		if key != "" && strings.Contains(out, key) {
			t.Errorf("key %q: the envelope repeats it: %s", key, out)
		}
	}
}

// The flag is given once, and only with --stdin: a listing is not billed.
func TestTheIdempotencyKeyFlagIsGivenOnceAndOnlyWithStdin(t *testing.T) {
	fr, cfg, _ := newFakeRouter(t, okHandler(decideModelsAnswer))
	for _, args := range [][]string{
		{"--models", "-idempotency-key", "k"},
		{"--stdin", "-idempotency-key", "k", "-idempotency-key", "k"},
	} {
		code, out, _ := runDecide(t, map[string]string{"JEVLIN_API_KEY": "sr-fictional-key"}, decideMinimal, append(args, "-config", cfg)...)
		env := decodeEnvelope(t, out)
		if code != exitUsage || env["code"] != codeInvalidFlags || env["action"] != actionFixInput {
			t.Errorf("%v: exit %d, %v / %v; want %d, invalid_flags / fix_input", args, code, env["code"], env["action"], exitUsage)
		}
	}
	if len(fr.reqs) != 0 {
		t.Errorf("the router received %d requests", len(fr.reqs))
	}
}

// The router's answers about a key: still running is a retry, after the
// Retry-After it sends; reused with another body is the caller's to fix.
// Each is one request.
func TestTheRoutersIdempotencyAnswersMapToTheirActions(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		exit       int
		code       string
		retryable  bool
		action     string
		retryAfter bool
	}{
		{"still running", `{"code":"idempotency_in_progress","error":"a request with this Idempotency-Key is still running"}`, 409, exitClientErr, "idempotency_in_progress", true, actionRetry, true},
		{"another 409", `{"code":"conflict","error":"no"}`, 409, exitClientErr, "conflict", false, actionReport, false},
		{"reused with another body", `{"code":"idempotency_key_reused","error":"this Idempotency-Key was already used with a different request body"}`, 422, exitClientErr, "idempotency_key_reused", false, actionFixInput, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, code, fr := decideServingHeaders(t, tc.status, tc.body, decideMinimal, map[string]string{"Retry-After": "1"}, "--stdin")
			if code != tc.exit || env["code"] != tc.code || env["retryable"] != tc.retryable || env["action"] != tc.action {
				t.Errorf("exit %d, %v / retryable %v / %v; want %d, %s / %v / %s",
					code, env["code"], env["retryable"], env["action"], tc.exit, tc.code, tc.retryable, tc.action)
			}
			if got, has := env["retry_after_ms"]; has != tc.retryAfter || (has && got != float64(1000)) {
				t.Errorf("retry_after_ms %v (present %v), want present %v", got, has, tc.retryAfter)
			}
			if len(fr.reqs) != 1 {
				t.Errorf("%d requests; one send per decide; the router answered each", len(fr.reqs))
			}
		})
	}
}

// The help names the flag.
func TestDecideHelpNamesTheIdempotencyKey(t *testing.T) {
	if !strings.Contains(decideUsage, "-idempotency-key key") || !strings.Contains(decideUsage, "search_fields") ||
		!strings.Contains(decideUsage, "consulted_pages") {
		t.Errorf("decide's help does not name -idempotency-key, search_fields and consulted_pages:\n%s", decideUsage)
	}
}
