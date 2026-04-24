//go:build !windows

package server

import (
	"os"
	"syscall"
)

// IsProcessAlive reports whether a process with the given PID exists.
// On Unix, os.FindProcess always succeeds for any PID, so we probe
// liveness by sending signal 0: the kernel validates the PID exists
// and we have permission to signal it, but delivers nothing.
func IsProcessAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
