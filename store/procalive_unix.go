//go:build !windows

package store

import (
	"os"
	"syscall"
)

// processAlive reports whether a process with the given PID exists.
// On Unix, os.FindProcess always succeeds for any PID, so liveness is
// probed by sending signal 0: the kernel validates the PID exists and
// we have permission to signal it, but delivers nothing.
//
// Mirrors server.IsProcessAlive. Deliberately duplicated rather than
// imported: dependencies flow inward (docs/architecture.md), so the
// store support package must not import the server transport layer.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
