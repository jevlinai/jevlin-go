//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// spawnDetached starts exe in its own session with no terminal, so the
// parent may exit (and the agent's tool call return) while the child
// finishes. Output goes nowhere: a detached child reports through its exit
// code and the next `status`, never through a stream nobody is reading.
func spawnDetached(exe string, args []string) error {
	cmd := exec.Command(exe, args...) // #nosec G204 G702 -- our own executable, fixed arguments
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Wait for it off to the side, never in the caller's path. A child that
	// exits while its parent still runs stays a zombie until the parent
	// reaps it; Setsid changes its session, not its parent. A one-shot
	// command exits first, and init adopts and reaps the child then, so the
	// goroutine simply ends with the process; `jevlin mcp` outlives every
	// search it serves, and without this each resume a search started
	// stayed in the process table until the host closed the server.
	go func() { _ = cmd.Wait() }()
	return nil
}
