package auth

import (
	"errors"
	"strings"

	"github.com/jevlinai/jevlin-go/internal/termtext"
)

// ValidAgentID is the shape an agent id must have before this client puts it
// in a URL path or a record: the platform's ids are opaque, but the client
// builds GET /v1/agents/{id} and POST /v1/agents/{id}/claim-code from them,
// and a few spellings name a different route there. "me" is the router's own
// self-lookup, which answers for whatever agent the key belongs to, so a
// record naming "me" passed every check that asks the platform about the
// record's agent and was never rebuilt. "." and ".." are path segments a
// server may resolve, and a separator splits the id into two. A control,
// format or separator character is termtext's rule, because the id is
// printed. The platform's own ids are none of these, so refusing them costs
// a real registration nothing.
func ValidAgentID(id string) error {
	switch {
	case id == "":
		return errors.New("auth: agent id is empty")
	case id == "me" || id == "." || id == "..":
		return errors.New("auth: agent id names a route, not an agent")
	case strings.ContainsAny(id, `/\`):
		return errors.New("auth: agent id holds a path separator")
	case termtext.HasControlChar(id):
		return errors.New("auth: agent id holds a control, format or separator character")
	}
	return nil
}
