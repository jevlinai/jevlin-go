package config

// Every network default the binary carries, in one place: the router, the
// platform and the agents API. A network cutover changes these literals and
// nothing else, because two hosts of truth drift apart and nothing notices
// a miss. TestDefaultsAreJevlin asserts every URL here is https.
const (
	DefaultRouterURL = "https://api.jevlin.ai"
	// DefaultPlatformBaseURL is the search platform's human-facing portal
	// and claim pages: what a printed claim_url is checked against
	// (invariant 12). It is NOT where connect's own requests go — see
	// DefaultAgentsAPIURL for that.
	DefaultPlatformBaseURL = "https://platform.jevlin.ai"
	// DefaultAgentsAPIURL is where connect actually sends register and
	// status. Confirmed live on the nyks.dev deployment: a register call
	// against the portal (platform.nyks.dev) 404'd — that route only
	// existed on the separate agents host — while the claim_url it
	// returned pointed back at the portal. One shared origin was this
	// package's original assumption; the real deployment splits it in two,
	// and the jevlin.ai hosts keep the split, with claim links on
	// platform.jevlin.ai.
	DefaultAgentsAPIURL = "https://agents-v1.jevlin.ai"
)
