package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gramaton-ai/gramaton/server"
)

// Reap timing. Vars (not consts) so tests can shrink the grace
// period instead of sleeping through it.
var (
	// reapWait is how long the reaper waits for a proxy to exit
	// after each signal before moving on (SIGTERM -> escalate to
	// kill -> give up).
	reapWait = 2 * time.Second
	// reapPollStep is the liveness polling interval within reapWait.
	reapPollStep = 50 * time.Millisecond
)

// procOps abstracts process inspection and signalling so the reap
// logic is testable without killing real processes.
type procOps struct {
	alive             func(pid int) bool
	looksLikeGramaton func(pid int) bool
	protected         func(pid int) bool  // never signal these (the reaper itself, the live server)
	terminate         func(pid int) error // graceful; SIGTERM on unix, Kill on Windows
	kill              func(pid int) error // forceful escalation
}

func defaultProcOps(cfgDir string) procOps {
	self := os.Getpid()
	serverPID := 0
	if info, err := server.ReadServerInfo(cfgDir); err == nil {
		serverPID = info.PID
	}
	return procOps{
		alive:             server.IsProcessAlive,
		looksLikeGramaton: processLooksLikeGramaton,
		protected: func(pid int) bool {
			return pid == self || (serverPID != 0 && pid == serverPID)
		},
		terminate: terminateProcess,
		kill:      killProcess,
	}
}

// reapAction is the per-entry decision when stopping proxies.
type reapAction int

const (
	// reapPrune: the PID is dead; remove the stale registry entry,
	// nothing to signal.
	reapPrune reapAction = iota
	// reapSkip: the PID is alive but does not look like a gramaton
	// process -- the registered proxy died and the OS recycled its
	// PID. Never signal it; warn and leave the entry for
	// ListMCPProxies to prune once the unrelated process exits.
	reapSkip
	// reapTerminate: alive and plausibly gramaton; signal it.
	reapTerminate
)

// reapDecision maps the per-entry process state to an action. The
// looksLikeGramaton leg is the PID-reuse guard: registry entries can
// outlive their proxy (SIGKILL, harness crash), and the OS is free
// to hand the recorded PID to an arbitrary process afterwards.
func reapDecision(alive, looksLikeGramaton bool) reapAction {
	switch {
	case !alive:
		return reapPrune
	case !looksLikeGramaton:
		return reapSkip
	default:
		return reapTerminate
	}
}

// reapMCPProxies stops the registered MCP proxy processes for the
// store at cfgDir and returns the number of proxies stopped.
// Warnings for entries it refuses to act on go to w.
//
// Registry entries whose PID is already dead are pruned inside
// ListMCPProxies; entries for successfully terminated proxies are
// pruned here after the kill (the proxy's own deferred cleanup
// usually beats us to it, and RemoveMCPProxy tolerates that).
//
// The reaper's own PID and the live server's PID are never
// signalled, even if a recycled PID lands a registry entry on one
// of them: SIGTERMing ourselves would abort the stop mid-run, and
// signalling the server out-of-band would race stopServer's
// graceful path. Such entries are skipped and left for
// ListMCPProxies to prune once the real owner exits.
func reapMCPProxies(cfgDir string, ops procOps, w io.Writer) int {
	stopped := 0
	for _, p := range server.ListMCPProxies(cfgDir) {
		if ops.protected != nil && ops.protected(p.PID) {
			fmt.Fprintf(w, "warning: registered mcp proxy pid %d belongs to the stop or server process (pid reused?); not signalling it\n", p.PID)
			continue
		}
		switch reapDecision(ops.alive(p.PID), ops.looksLikeGramaton(p.PID)) {
		case reapPrune:
			// Died between the ListMCPProxies liveness probe and
			// now; List prunes on its next read, but the entry is
			// known-stale so collect it here.
			server.RemoveMCPProxy(cfgDir, p.PID)
		case reapSkip:
			fmt.Fprintf(w, "warning: registered mcp proxy pid %d is not a gramaton process (pid reused?); not signalling it\n", p.PID)
		case reapTerminate:
			if terminateWithEscalation(p.PID, ops) {
				server.RemoveMCPProxy(cfgDir, p.PID)
				stopped++
			} else {
				fmt.Fprintf(w, "warning: mcp proxy pid %d did not exit; leaving its registry entry\n", p.PID)
			}
		}
	}
	return stopped
}

// terminateWithEscalation asks a process to exit gracefully, waits
// up to reapWait, then escalates to a forceful kill and waits again.
// Returns true once the process is gone. Signal errors are ignored:
// the usual cause is the process exiting between the liveness check
// and the signal, and the liveness poll is the arbiter either way.
func terminateWithEscalation(pid int, ops procOps) bool {
	_ = ops.terminate(pid)
	if waitForExit(pid, ops.alive, reapWait) {
		return true
	}
	_ = ops.kill(pid)
	return waitForExit(pid, ops.alive, reapWait)
}

// waitForExit polls until the PID is gone or the timeout elapses.
func waitForExit(pid int, alive func(int) bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !alive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(reapPollStep)
	}
}

// processLooksLikeGramaton reports whether the process with the
// given PID plausibly runs a gramaton binary. This is the PID-reuse
// guard for the reaper: it fails closed (returns false) whenever the
// inspection tool errors, so an unverifiable process is skipped
// rather than killed.
//
// On unix the check is `ps -p <pid> -o comm=` and a substring match
// on "gramaton" (Linux truncates comm to 15 bytes, which still fits;
// macOS reports the full executable path, hence the Base). On
// Windows it is a best-effort `tasklist` image-name match. Residual
// risk on every platform: a recycled PID whose new owner also has
// "gramaton" in its name would be signalled. The started_at field in
// the registry entry is recorded for observability (status output);
// the reap path does not consult it.
func processLooksLikeGramaton(pid int) bool {
	if runtime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").Output()
		if err != nil {
			return false
		}
		return strings.Contains(strings.ToLower(string(out)), "gramaton")
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return false
	}
	comm := filepath.Base(strings.TrimSpace(string(out)))
	return strings.Contains(strings.ToLower(comm), "gramaton")
}

// terminateProcess asks a process to exit gracefully. On unix that
// is SIGTERM: the proxy's signal.NotifyContext (cli/mcp_cmd.go)
// turns it into a clean shutdown that removes its own registry
// entry. Windows has no SIGTERM delivery; Process.Kill is the only
// portable termination there.
func terminateProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return p.Kill()
	}
	return p.Signal(syscall.SIGTERM)
}

// killProcess forcefully terminates a process (SIGKILL on unix).
func killProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
