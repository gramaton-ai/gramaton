//go:build windows

package ollama

import "os/exec"

// detachProcess is a no-op on Windows; CreateNewProcessGroup is the
// rough equivalent but ollama on Windows is not officially supported
// in Gramaton's auto-start path today.
func detachProcess(cmd *exec.Cmd) {}
