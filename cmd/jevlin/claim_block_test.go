package main

import (
	"strings"
	"testing"
)

func TestTheClaimBlockSaysEachStepAndWhereTheCodeIsTyped(t *testing.T) {
	got := claimBlock("https://platform.jevlin.ai/claim/AB12CD34", "AB12CD34", "https://platform.jevlin.ai")
	want := "This agent is registered. Free search works now, within an unclaimed agent's limits:\n" +
		"the fast tier only, with a daily cap. Claiming it lifts the limits and opens the other tiers.\n" +
		"To claim it:\n" +
		"  1. Open this link in a browser:  https://platform.jevlin.ai/claim/AB12CD34\n" +
		"  2. Sign in, or create an account; you come back to this page.\n" +
		"  3. Check the agent's name and confirm.\n" +
		"If the link does not open, go to https://platform.jevlin.ai/claim and type the code AB12-CD34.\n" +
		"The link is valid for a week; `jevlin connect` prints a fresh one after that."
	if got != want {
		t.Errorf("the claim block:\n%s\nwant:\n%s", got, want)
	}
}

func TestTheClaimCodeIsShownInThePlatformsHyphenatedForm(t *testing.T) {
	for in, want := range map[string]string{
		"AB12CD34":  "AB12-CD34",
		"AB12-CD34": "AB12-CD34",
		"ab12cd34":  "AB12-CD34",
		"MINT-01":   "MINT-01", // not eight characters: shown as issued
	} {
		if got := hyphenatedClaimCode(in); got != want {
			t.Errorf("hyphenatedClaimCode(%q) = %q, want %q", in, got, want)
		}
	}
}

// The typed-code address is the configured [platform] url's origin, whatever
// path that url carries, and never a literal host.
func TestTheClaimPageAddressIsTheConfiguredOrigin(t *testing.T) {
	got := claimBlock("https://portal.example.test/claim/AB12CD34", "AB12CD34", "https://portal.example.test/some/path/")
	if !strings.Contains(got, "go to https://portal.example.test/claim and type the code") {
		t.Errorf("the typed-code address is not the configured origin:\n%s", got)
	}
	if strings.Contains(got, "platform.jevlin.ai") {
		t.Errorf("the block names a literal host:\n%s", got)
	}
	// Nothing to build the address from, or nothing to type: the line is left out.
	for _, c := range []struct{ code, base string }{{"AB12CD34", ""}, {"", "https://portal.example.test"}} {
		if strings.Contains(claimBlock("https://portal.example.test/claim/x", c.code, c.base), "type the code") {
			t.Errorf("a typed-code line without its address or code: %+v", c)
		}
	}
}

func TestTheClaimBlockPrintsNoNumberForTheDailyCap(t *testing.T) {
	for _, line := range strings.Split(claimBlock("https://p.test/claim/AB12CD34", "AB12CD34", "https://p.test"), "\n")[:2] {
		if strings.ContainsAny(line, "0123456789$") {
			t.Errorf("the limits sentence carries a number: %q", line)
		}
	}
}

func TestConnectPrintsTheClaimBlockWithTheRealLinkAndCode(t *testing.T) {
	platform, cfgPath, _, _, claimURL, claimCode := connectedUnclaimed(t)
	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d: %s", code, errOut)
	}
	block := claimBlock(claimURL, claimCode, platform.srv.URL)
	if !strings.Contains(out, block+"\n") {
		t.Errorf("connect did not print the whole block:\n%s\nwant it to contain:\n%s", out, block)
	}
	if !strings.Contains(out, "go to "+platform.srv.URL+"/claim and type the code "+hyphenatedClaimCode(claimCode)) {
		t.Errorf("the typed-code line does not name the configured origin and the hyphenated code:\n%s", out)
	}
	if !strings.Contains(out, claimTimeoutMessage) {
		t.Errorf("the timeout line is missing:\n%s", out)
	}
}

func TestAClaimedAgentGetsNoClaimBlock(t *testing.T) {
	platform, cfgPath, _, _, _, _ := connectedUnclaimed(t)
	platform.claim("search")
	code, out, errOut := runConnect(t, cfgPath, nil)
	if code != exitOK {
		t.Fatalf("connect exited %d: %s", code, errOut)
	}
	for _, s := range []string{"This agent is registered.", "type the code", "Not claimed yet.", "/claim"} {
		if strings.Contains(out, s) {
			t.Errorf("a claimed agent was shown %q:\n%s", s, out)
		}
	}
}
