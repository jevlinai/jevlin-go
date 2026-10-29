package main

import (
	"bytes"
	"strings"
	"testing"
)

// A wallet node URL carrying a user or password is refused, and the refusal
// does not repeat it: it would go to the node as Basic authorization on every
// request, from a value sitting in argv or the environment.
func TestAWalletNodeURLWithUserinfoIsRefusedWithoutRepeatingIt(t *testing.T) {
	for _, raw := range []string{
		"https://user:s3cretpass@rpc.example:443",
		"https://s3cretpass@rpc.example",
		"http://user:s3cretpass@127.0.0.1:26657",
	} {
		for _, insecure := range []bool{false, true} {
			var stderr bytes.Buffer
			err := validateNodeURL(raw, insecure, &stderr)
			if err == nil {
				t.Errorf("%s (insecure=%v) was accepted", raw, insecure)
				continue
			}
			if strings.Contains(err.Error()+stderr.String(), "s3cretpass") {
				t.Errorf("the refusal repeats the credential: %v %s", err, stderr.String())
			}
		}
	}
	var stderr bytes.Buffer
	if err := validateNodeURL("https://rpc.example", false, &stderr); err != nil {
		t.Errorf("a plain https node was refused: %v", err)
	}
}
