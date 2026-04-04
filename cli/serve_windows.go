//go:build windows

package cli

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr configures the subprocess to detach on Windows.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
