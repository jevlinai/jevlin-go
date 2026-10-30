package auth

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

const echoedSecret = "sr-echoedkey0123456789abcdef0123456789" // #nosec G101 -- a synthetic key a stub echoes back

// What an AS says in a refusal is printed by a foreground command; a key or
// token it echoes is redacted first, and the refusal keeps its code.
func TestAnASRefusalsServerTextIsRedacted(t *testing.T) {
	err := joinRefusal(403, []byte(`{"error":{"code":"FORBIDDEN","message":"token `+echoedSecret+` is not allowed"}}`))
	var refused *ASRefusedError
	if !errors.As(err, &refused) || refused.Code != "FORBIDDEN" {
		t.Fatalf("the refusal lost its code: %v", err)
	}
	if strings.Contains(err.Error(), echoedSecret) {
		t.Errorf("an AS refusal repeats what the server echoed: %v", err)
	}
}

// x/oauth2 carries the token endpoint's own description, URI and body; each is
// redacted and the error keeps its type, so callers still branch on it.
func TestATokenEndpointRefusalIsRedactedAndKeepsItsType(t *testing.T) {
	in := &oauth2.RetrieveError{
		ErrorCode:        "invalid_grant",
		ErrorDescription: "refresh token " + echoedSecret + " is revoked",
		ErrorURI:         "https://as.example/help?token=" + echoedSecret,
		Body:             []byte(`{"error":"invalid_grant","echo":"` + echoedSecret + `"}`),
	}
	out := scrubRetrieveError(in)
	var re *oauth2.RetrieveError
	if !errors.As(out, &re) || re.ErrorCode != "invalid_grant" {
		t.Fatalf("the scrubbed error lost its type or code: %v", out)
	}
	for _, s := range []string{out.Error(), re.ErrorDescription, re.ErrorURI, string(re.Body)} {
		if strings.Contains(s, echoedSecret) {
			t.Errorf("a token endpoint's refusal repeats what it echoed: %s", s)
		}
	}
	if in.ErrorDescription == re.ErrorDescription {
		t.Error("the original error was redacted in place, or not at all")
	}
}

// A token endpoint that answers with no error code is described by its
// status, which the scrubbed error keeps, and never by a panic.
func TestATokenEndpointRefusalWithNoCodeStillSaysItsStatus(t *testing.T) {
	in := &oauth2.RetrieveError{
		Response: &http.Response{Status: "502 Bad Gateway", StatusCode: http.StatusBadGateway, Header: http.Header{"Set-Cookie": {"s=" + echoedSecret}}},
		Body:     []byte("upstream echoed " + echoedSecret),
	}
	var msg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Error() panicked: %v", r)
			}
		}()
		msg = scrubRetrieveError(in).Error()
	}()
	if !strings.Contains(msg, "502 Bad Gateway") || strings.Contains(msg, echoedSecret) {
		t.Errorf("the scrubbed error: %q", msg)
	}
}
