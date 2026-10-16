package config

// Every network default the binary carries, in one place: the chain, the
// AS, the router, the platform, the agents API and the wallet's node. A
// network cutover changes these literals and nothing else, because two
// hosts of truth drift apart and nothing notices a miss.
// TestDefaultsAreTestnet asserts every value here is still the testnet
// one and every URL is https.
const (
	DefaultChainID = "twilight-testnet-1"
	DefaultSlotID  = 3
	// DefaultASBaseURL moved from https://rewards.nyks.dev on 2026-10-16.
	// The AS origin is configured, never discovered (invariant 5), and
	// setup writes it into the config, which nothing rewrites: an
	// installation set up before then keeps the old origin until its
	// as_url line is changed by hand, and the old host's documents now
	// name this one, which that installation refuses as a different AS.
	DefaultASBaseURL = "https://rewards-testnet.jevlin.ai"
	DefaultRouterURL = "https://api.jevlin.ai"
	// DefaultPlatformBaseURL is the search platform's human-facing portal
	// and claim pages: what a printed claim_url is checked against
	// (invariant 12). It is NOT where connect/mining enable's own
	// requests go — see DefaultAgentsAPIURL for that.
	DefaultPlatformBaseURL = "https://platform.jevlin.ai"
	// DefaultAgentsAPIURL is where connect/mining enable actually send
	// register/status/enroll. Confirmed live on the nyks.dev deployment: a
	// register call against the portal (platform.nyks.dev) 404'd — that
	// route only existed on the separate agents host — while the claim_url
	// it returned pointed back at the portal. One shared origin was this
	// package's original assumption; the real deployment splits it in two,
	// and the jevlin.ai hosts keep the split, with claim links on
	// platform.jevlin.ai.
	DefaultAgentsAPIURL = "https://agents-v1.jevlin.ai"
	// LegacyPlatformBaseURL / LegacyAgentsAPIURL are the pair the platform
	// lived at before jevlin.ai. A config written then may name only the
	// portal and rely on the agents API being implied from it; it still
	// is, as this pair, so that config keeps loading after an upgrade
	// rather than being refused as a custom portal with no API named
	// (PR #87 review). Both hosts still answer; a participant moves to the
	// new ones by editing the config — running setup again does not, since
	// setup leaves an existing config byte-identical. The portal's claim
	// links already come on the new host, which pkg/platform accepts for
	// this old base_url (retiredPortalSuccessors).
	LegacyPlatformBaseURL = "https://platform.nyks.dev"
	LegacyAgentsAPIURL    = "https://agents-v1.nyks.dev"
	DefaultWalletDenom    = "utwlt"
	DefaultBech32HRP      = "twilight"
)

// DefaultWalletNodes is keyed by chain id: the CometBFT RPC node the
// wallet uses when nothing overrides it (-node, then
// JEVLIN_WALLET_NODE, then this table). One row per chain we know; a
// chain with no row has no default — the wallet refuses before any
// network call rather than guessing. Adding a network is adding a row
// here.
//
// There is deliberately no liveness pre-flight for these: a dead node
// fails the first request with the existing "cannot reach the node"
// message, and a live node on the wrong chain fails the wallet's own
// chain-id comparison before it ever signs. That comparison — not this
// table — is what makes carrying a default safe across this testnet,
// later testnets, and mainnet: the client can never sign for a node
// whose chain id is not the one it was configured for.
var DefaultWalletNodes = map[string]string{
	DefaultChainID: "https://rpc.nyks.dev",
}
