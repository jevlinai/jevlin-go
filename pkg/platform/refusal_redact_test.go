package platform

import (
	"strings"
	"testing"
)

// Text a platform sends back in a refusal is printed to the participant's
// terminal or a CI log; a key or token it echoes is redacted first.
func TestARefusalsServerTextIsRedacted(t *testing.T) {
	const echoed = "sr-echoedkey0123456789abcdef0123456789" // #nosec G101 -- a synthetic key a stub echoes back
	err := refusal(400, []byte(`{"code":"bad_request","error":{"code":"bad_request","message":"the key `+echoed+` and Bearer abcdefghijklmnopqrstuv were refused"}}`))
	msg := err.Error()
	if strings.Contains(msg, echoed) || strings.Contains(msg, "abcdefghijklmnopqrstuv") {
		t.Fatalf("a refusal repeats what the server echoed: %s", msg)
	}
	if !strings.Contains(msg, "bad_request") || !strings.Contains(msg, "[REDACTED]") {
		t.Errorf("the refusal lost its code or does not say what was removed: %s", msg)
	}
}
