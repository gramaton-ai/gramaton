//go:build !windows

package ollama

import (
	"os/exec"
	"syscall"
)

// detachProcess configures cmd so the subprocess starts in its own
// process group. SIGINT/SIGHUP delivered to the gramaton process group
// do NOT propagate to the child; ollama keeps running across gramaton
// restarts.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
