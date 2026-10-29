package auth

import (
	"testing"

	"github.com/jevlinai/jevlin-go/internal/egress"
)

// testOrigin is raw's origin, as a refresh authorization records its AS.
func testOrigin(t *testing.T, raw string) string {
	t.Helper()
	o, err := egress.Origin(raw)
	if err != nil {
		t.Fatalf("origin of %q: %v", raw, err)
	}
	return o
}

// testASOrigin is the origin the store tests record a token with when no AS
// is in play.
const testASOrigin = "https://as.example"
