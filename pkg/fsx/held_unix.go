//go:build !windows

package fsx

// heldBriefly is constant false: a POSIX rename is not refused because
// another process has the target open, so there is nothing to wait out.
func heldBriefly(error) bool { return false }
