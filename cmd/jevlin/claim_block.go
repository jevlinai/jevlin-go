package main

// What a person is told when their agent is registered and not yet claimed.
// One block, printed by connect (which setup runs), so the steps, where the
// code is typed and what an unclaimed agent may do are said once, in one
// place. The daily cap has no number here on purpose: it is decided by the
// router, whose /v1/limits is the authority.

import (
	"net/url"
	"strings"
)

// claimBlock is the claim instructions for the link the platform issued.
// claimURL has passed printableClaimURL; baseURL is the configured
// [platform] url, and the typed-code address is built from its origin, never
// from a literal host. With no code on file the typed-code line is left out.
func claimBlock(claimURL, claimCode, baseURL string) string {
	var b strings.Builder
	b.WriteString("This agent is registered. Free search works now, within an unclaimed agent's limits:\n")
	b.WriteString("the fast tier only, with a daily cap. Claiming it lifts the limits and opens the other tiers.\n")
	b.WriteString("To claim it:\n")
	b.WriteString("  1. Open this link in a browser:  " + claimURL + "\n")
	b.WriteString("  2. Sign in, or create an account; you come back to this page.\n")
	b.WriteString("  3. Check the agent's name and confirm.\n")
	if origin := claimOrigin(baseURL); origin != "" && claimCode != "" {
		b.WriteString("If the link does not open, go to " + origin + "/claim and type the code " + hyphenatedClaimCode(claimCode) + ".\n")
	}
	b.WriteString("The link is valid for a week; `jevlin connect` prints a fresh one after that.")
	return b.String()
}

// claimOrigin is scheme://host of the configured [platform] url.
func claimOrigin(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// hyphenatedClaimCode spells an eight-character code the way the platform
// shows it, AB12-CD34. The claim page ignores case and the hyphen, so a code
// of any other length is shown as it was issued.
func hyphenatedClaimCode(code string) string {
	plain := strings.ToUpper(strings.ReplaceAll(code, "-", ""))
	if len(plain) != 8 {
		return code
	}
	return plain[:4] + "-" + plain[4:]
}

// claimTimeoutMessage ends a foreground connect whose poll ran out with the
// agent still unclaimed and a link on file.
const claimTimeoutMessage = "Not claimed yet. Finish the steps above whenever you like: search keeps working, " +
	"and the next `jevlin search` or `jevlin connect` completes the claim."
