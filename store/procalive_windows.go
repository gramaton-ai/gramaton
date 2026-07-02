//go:build windows

package store

import "golang.org/x/sys/windows"

// processAlive reports whether a process with the given PID exists.
// Windows has no Signal(0) equivalent: we ask the kernel for a
// process handle with the minimum rights
// (PROCESS_QUERY_LIMITED_INFORMATION). OpenProcess returns an error
// if the PID is not in the process table; success means a live
// process. The handle is closed immediately -- only whether acquiring
// it succeeded matters.
//
// Mirrors server.IsProcessAlive. Deliberately duplicated rather than
// imported: dependencies flow inward (docs/architecture.md), so the
// store support package must not import the server transport layer.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = windows.CloseHandle(h)
	return true
}
