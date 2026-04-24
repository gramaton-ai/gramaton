//go:build windows

package server

import "golang.org/x/sys/windows"

// IsProcessAlive reports whether a process with the given PID exists.
// Windows has no Signal(0) equivalent: we ask the kernel for a
// process handle with the minimum rights (PROCESS_QUERY_LIMITED_INFORMATION).
// OpenProcess returns an error if the PID is not in the process
// table; success means a live process. We immediately close the
// handle — we only care about whether acquiring it succeeded.
func IsProcessAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = windows.CloseHandle(h)
	return true
}
