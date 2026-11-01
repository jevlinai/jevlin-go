//go:build !windows

package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// A host that stops the server with SIGTERM while a search is running stops
// the server: the process ends by the signal, and the request the host
// sends after it is never answered. Run as the real binary, because the
// claim is about what the signal does to the process.
func TestASignalMidSearchEndsTheServer(t *testing.T) {
	bin := testBuildOfJevlin(t)
	var pid atomic.Int64
	hit := make(chan struct{}, 1)
	_, cfg, root := newFakeRouter(t, func(_ http.ResponseWriter, r *http.Request) {
		if p := pid.Load(); p > 0 {
			_ = syscall.Kill(int(p), syscall.SIGTERM)
		}
		select {
		case hit <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})

	cmd := exec.Command(bin, "mcp", "-config", cfg) // #nosec G204 -- this test's own build and config
	cmd.Env = []string{
		"HOME=" + root,
		"JEVLIN_HOME=" + filepath.Join(root, "home"),
		"JEVLIN_CONFIG=" + cfg,
		"JEVLIN_API_KEY=sr-fictional-key",
		"PATH=" + os.Getenv("PATH"),
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid.Store(int64(cmd.Process.Pid))
	if _, err := io.WriteString(stdin, mcpInitialize+"\n"+toolCall(2, "search", `{"query":"a search a host stops"}`)+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hit:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the search never reached the router")
	}
	// The process may be gone already, so a failed write is expected.
	_, _ = io.WriteString(stdin, `{"jsonrpc":"2.0","id":3,"method":"ping"}`+"\n")
	_ = stdin.Close()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the server was still running 30 seconds after SIGTERM")
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("the server exited %v after SIGTERM, want ended by the signal; stdout:\n%s", err, out.String())
	}
	if ws, ok := exit.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Errorf("the server ended %v, want by SIGTERM; stdout:\n%s", exit, out.String())
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, `"id":3`) || strings.Contains(line, `"id":2`) {
			t.Errorf("the server answered after SIGTERM: %s", line)
		}
	}
}
