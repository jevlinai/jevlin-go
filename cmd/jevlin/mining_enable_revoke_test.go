package main

import (
	"bufio"
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// `mining enable` cancels a revoke `mining disable` left pending, or the
// next flush revokes the session the command just said stays on. The
// enabled branch that reports an address already on file returned before
// finishMiningEnabled's clear, and the structural guard's flush found the
// installation "not enrolled" after it.
func TestMiningEnableOnAnEnabledInstallationCancelsAPendingRevoke(t *testing.T) {
	for _, interactive := range []bool{true, false} {
		platform := newStubPlatform(t)
		cfgPath, stateDir := connectConfig(t, platform.srv.URL, "")
		cfg := mustLoadConfig(t, cfgPath)
		store, err := auth.OpenStore(stateDir)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveMiningEnabled(true); err != nil {
			t.Fatal(err)
		}
		if err := testPayoutRecord(t, stateDir).Save(participantAddress); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveRevokePending(); err != nil {
			t.Fatal(err)
		}
		in := strings.NewReader("")
		outcome, code := miningEnableDecision(in, bufio.NewReader(in), &bytes.Buffer{}, &bytes.Buffer{}, os.Getenv, cfg, store, interactive, false)
		if code != exitOK || !outcome.enabled {
			t.Fatalf("interactive=%v: exit %d, outcome %+v", interactive, code, outcome)
		}
		if pending, err := store.LoadRevokePending(); err != nil || pending {
			t.Fatalf("interactive=%v: the pending revoke survived `mining enable`: pending=%v err=%v", interactive, pending, err)
		}
	}
}
