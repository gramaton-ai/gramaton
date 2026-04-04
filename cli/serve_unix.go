//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr configures the subprocess to detach from the terminal.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
}
