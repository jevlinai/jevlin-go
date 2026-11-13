package main

// What a person is told when their agent is registered and not yet claimed.
// One block, printed by connect (which setup runs), so the steps, where the
// code is typed and what an unclaimed agent may do are said once, in one
// place. The daily cap has no number here on purpose: it is decided by the
// router, whose /v1/limits is the authority.

import (
	"fmt"
	"net/url"
	"strings"
	"time"
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

// claimWaitMessage is the one line a foreground connect prints when its poll
// starts waiting for the claim, because a wait that says nothing reads as a
// hang, and a person who presses Ctrl-C to find out must know beforehand what
// that does. Both forms say what a timeout does and what Ctrl-C does, and say
// it truthfully for the caller. In setup an interrupted wait is connect
// failing: setup stops there and its environment and coding agents steps do
// not run, while a wait that runs out lets setup go on. On its own, connect
// interrupted leaves the registration and the key in place, and the claim is
// completed later as it is after a timeout. underSetup is connect's admitted
// run, whose one caller is setup. The budget is connectPollBudget's, so the
// number printed is the bound the loop keeps.
func claimWaitMessage(budget time.Duration, underSetup bool) string {
	wait := "Waiting up to " + waitBudgetText(budget) + " for the claim"
	if underSetup {
		return wait + ", then setup goes on without it. Ctrl-C ends setup here, before its environment " +
			"and coding agents steps; run setup again to finish them."
	}
	return wait + ". Ctrl-C stops waiting; once you have confirmed, the next `jevlin search` or " +
		"`jevlin connect` completes the claim."
}

// waitBudgetText spells a poll budget the way a person reads it: whole
// minutes or seconds in words, anything else as Go writes a duration.
func waitBudgetText(d time.Duration) string {
	unit := func(n int64, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %ss", n, one)
	}
	switch {
	case d >= time.Minute && d%time.Minute == 0:
		return unit(int64(d/time.Minute), "minute")
	case d >= time.Second && d%time.Second == 0:
		return unit(int64(d/time.Second), "second")
	}
	return d.String()
}

// claimedTiersMessage follows "claimed" when a foreground connect saw the
// agent become claimed. Searches stay on the key's default, the fast tier,
// until a tier is chosen, and nothing else at that moment says that claiming
// opened the others. It names only the two a claim opens (balanced and max:
// tierLabel, and the tiers table in docs/reference.md), and calls them
// wider, not faster: fast is the quickest tier there is. Codex spells the
// skill's command with $ rather than /.
const claimedTiersMessage = "The balanced and max tiers are open to this agent now; they ask more engines than fast. " +
	"Pick one with `jevlin agents tier <tier>`, or `/jevlin tier <tier>` in your agent (`$jevlin` in Codex)."
