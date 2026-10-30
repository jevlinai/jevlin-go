package main

import "testing"

// The JavaScript adapters build their bridges themselves, with the
// assistant's text in them, so they read JEVLIN_TRACE as the binary does:
// with it off, Pi's extension leaves our search exactly as the agent wrote
// it. The opencode plugin shares the source and the check.
func TestPiExtensionWritesNoBridgeWhenTraceIsOff(t *testing.T) {
	entries := []any{piUser("find me a thing"), piAssistant("Looking that up now.", "c1")}
	call := []piCall{{Session: "s", Command: piSearchCommand, ToolCallID: "c1", Entries: entries}}
	// The control first: with the variable unset the search is bridged.
	t.Setenv("JEVLIN_TRACE", "")
	if got := runPiExtension(t, map[string][]piCall{"on": call}); piBridge(t, got["on"][0]) == nil {
		t.Fatal("the control did not bridge the search")
	}
	for _, value := range []string{"off", "OFF", "0", "false"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("JEVLIN_TRACE", value)
			got := runPiExtension(t, map[string][]piCall{"ours": call})
			piNoBridge(t, got["ours"][0], piSearchCommand)
		})
	}
}
