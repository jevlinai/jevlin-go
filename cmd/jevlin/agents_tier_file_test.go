package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jevlinai/jevlin-go/pkg/config"
)

// tierOnDisk runs `agents tier` with the real file operations against a
// real config, so the tier file beside it is a real file a test can plant.
func tierOnDisk(t *testing.T) (cfg string, run func(args ...string) (int, string, string)) {
	t.Helper()
	_, cfg, _ = newFakeRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(routerBody))
	})
	ops := realAgentOps()
	return cfg, func(args ...string) (int, string, string) {
		return runAgents(t, ops, nil, append([]string{"tier"}, append(args, "-config", cfg)...)...)
	}
}

// The command and status read the tier file exactly as search does: one
// bounded read of a regular file. A file search would refuse, here one
// longer than the bound, is unset to status as it is to search, so status
// never names a band the next search will not send, and setting a band
// over it writes rather than finding it already set.
func TestAgentsTierReadsTheFileAsSearchDoes(t *testing.T) {
	cfg, run := tierOnDisk(t)
	path := filepath.Join(filepath.Dir(cfg), tierFile)
	padded := "balanced" + strings.Repeat(" ", tierFileMaxBytes) + "\n"
	if err := os.WriteFile(path, []byte(padded), 0o600); err != nil { // #nosec G703 -- a path in this test's own temp dir
		t.Fatal(err)
	}
	if searchTierDefault(cfg, configSearchFor(t, cfg), os.Getenv) != "" {
		t.Fatal("precondition: search should not read a file over its bound")
	}
	if code, out, errOut := run("status"); code != exitOK || !strings.Contains(out, "unset") {
		t.Fatalf("status over an oversized file: %d %q %q, want unset as search reads it", code, out, errOut)
	}
	if code, _, errOut := run("balanced"); code != exitOK {
		t.Fatalf("set balanced: %d %q", code, errOut)
	}
	if got := searchTierDefault(cfg, configSearchFor(t, cfg), os.Getenv); got != "balanced" {
		t.Fatalf("after `agents tier balanced` search sends %q, want balanced", got)
	}
}

func configSearchFor(t *testing.T, cfgPath string) config.Search {
	t.Helper()
	cfg, _, err := loadConfig(cfgPath, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Search
}

// A [search] tier this client cannot use loads as no tier (pkg/config), is
// never sent, and is named by status, so a participant who wrote one sees
// why the router's default applies instead of a bare "unset".
func TestAnUnknownConfigTierIsNamedByStatusAndNotSent(t *testing.T) {
	cfg, run := tierOnDisk(t)
	raw, err := os.ReadFile(cfg) // #nosec G304 -- a path in this test's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(raw), "[search]\n", "[search]\ntier = \"turbo\"\n", 1)
	if body == string(raw) {
		t.Fatalf("precondition: no [search] table in %q", raw)
	}
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil { // #nosec G703 -- a path in this test's own temp dir
		t.Fatal(err)
	}
	if got := searchTierDefault(cfg, configSearchFor(t, cfg), os.Getenv); got != "" {
		t.Fatalf("search would send %q for an unknown [search] tier", got)
	}
	code, out, errOut := run("status")
	if code != exitOK || !strings.Contains(out, "unset") || !strings.Contains(out, `"turbo"`) {
		t.Fatalf("status with an unknown [search] tier: %d %q %q, want unset and the word named", code, out, errOut)
	}
}
