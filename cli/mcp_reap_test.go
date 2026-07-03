package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/server"
)

// deadPID is a PID that cannot refer to a live process on any
// supported platform: Linux caps PIDs at PID_MAX_LIMIT (4194304),
// macOS at 99998, and Windows PIDs stay far below this in practice.
const deadPID = 1 << 30

// writeProxyEntry writes a registry entry for an arbitrary PID using
// the documented registry shape (<config-dir>/mcp/<pid>.json).
// server.RegisterMCPProxy always registers the current process, so
// tests build entries for other PIDs by hand.
func writeProxyEntry(t *testing.T, cfgDir string, pid int) string {
	t.Helper()
	regDir := filepath.Join(cfgDir, "mcp")
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatalf("mkdir registry: %v", err)
	}
	info := server.MCPProxyInfo{PID: pid, StartedAt: time.Now().UTC(), Binary: "/fake/gramaton"}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(regDir, fmt.Sprintf("%d.json", pid))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	return path
}

func TestReapDecision(t *testing.T) {
	tests := []struct {
		name              string
		alive             bool
		looksLikeGramaton bool
		want              reapAction
	}{
		{"dead pid: prune only", false, false, reapPrune},
		{"dead pid, stale name match: still prune", false, true, reapPrune},
		{"alive but name mismatch: skip (pid reuse)", true, false, reapSkip},
		{"alive and gramaton: terminate", true, true, reapTerminate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reapDecision(tt.alive, tt.looksLikeGramaton); got != tt.want {
				t.Errorf("reapDecision(%v, %v) = %v, want %v", tt.alive, tt.looksLikeGramaton, got, tt.want)
			}
		})
	}
}

// stubOps returns injectable procOps that record signals instead of
// delivering them. terminate and kill mark the PID dead, so
// waitForExit returns on its first poll.
func stubOps(matchPID int) (procOps, *[]string) {
	var calls []string
	dead := map[int]bool{}
	ops := procOps{
		alive:             func(pid int) bool { return !dead[pid] },
		looksLikeGramaton: func(pid int) bool { return pid == matchPID },
		protected:         func(int) bool { return false },
		terminate: func(pid int) error {
			calls = append(calls, fmt.Sprintf("terminate:%d", pid))
			dead[pid] = true
			return nil
		},
		kill: func(pid int) error {
			calls = append(calls, fmt.Sprintf("kill:%d", pid))
			dead[pid] = true
			return nil
		},
	}
	return ops, &calls
}

func TestReapMCPProxiesMatrix(t *testing.T) {
	dir := t.TempDir()
	// os.Getpid: ListMCPProxies prunes with the REAL liveness check
	// before ops run, so the approved entry must be a genuinely live
	// process. Protection is stubbed off here; the self-guard has its
	// own tests (TestReapNeverSignalsSelf / TestDefaultProcOpsProtects).
	target := os.Getpid() // alive, name-check stub approves
	other := os.Getppid() // alive, name-check stub rejects (pid-reuse case)

	targetPath := writeProxyEntry(t, dir, target)
	otherPath := writeProxyEntry(t, dir, other)
	deadPath := writeProxyEntry(t, dir, deadPID)

	ops, calls := stubOps(target)
	var warnings bytes.Buffer
	stopped, _ := reapMCPProxies(dir, ops, &warnings)

	if stopped != 1 {
		t.Fatalf("stopped = %d, want 1", stopped)
	}
	if want := []string{fmt.Sprintf("terminate:%d", target)}; len(*calls) != 1 || (*calls)[0] != want[0] {
		t.Fatalf("signal calls = %v, want %v", *calls, want)
	}
	// Matched proxy: terminated and its entry pruned.
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Error("terminated proxy entry should be pruned")
	}
	// Name mismatch: warned, never signalled, entry left for
	// ListMCPProxies to collect once the PID frees up.
	if !bytes.Contains(warnings.Bytes(), []byte("not a gramaton process")) {
		t.Errorf("expected pid-reuse warning, got %q", warnings.String())
	}
	if _, err := os.Stat(otherPath); err != nil {
		t.Errorf("skipped entry should remain: %v", err)
	}
	// Dead PID: pruned (by ListMCPProxies) without any signal.
	if _, err := os.Stat(deadPath); !os.IsNotExist(err) {
		t.Error("dead-PID entry should be pruned")
	}
}

func TestReapEscalatesToKill(t *testing.T) {
	// Shrink the grace period so the escalation path doesn't cost
	// the test 2 seconds per wait.
	oldWait, oldStep := reapWait, reapPollStep
	reapWait, reapPollStep = 50*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { reapWait, reapPollStep = oldWait, oldStep })

	dir := t.TempDir()
	target := os.Getpid() // must be genuinely alive for ListMCPProxies
	writeProxyEntry(t, dir, target)

	var calls []string
	dead := false
	ops := procOps{
		alive:             func(int) bool { return !dead },
		looksLikeGramaton: func(int) bool { return true },
		protected:         func(int) bool { return false },
		terminate: func(pid int) error { // ignored by the "proxy"
			calls = append(calls, fmt.Sprintf("terminate:%d", pid))
			return nil
		},
		kill: func(pid int) error {
			calls = append(calls, fmt.Sprintf("kill:%d", pid))
			dead = true
			return nil
		},
	}

	var warnings bytes.Buffer
	stopped, _ := reapMCPProxies(dir, ops, &warnings)

	if stopped != 1 {
		t.Fatalf("stopped = %d, want 1", stopped)
	}
	want := []string{fmt.Sprintf("terminate:%d", target), fmt.Sprintf("kill:%d", target)}
	if len(calls) != 2 || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("signal calls = %v, want %v", calls, want)
	}
	if got := server.ListMCPProxies(dir); len(got) != 0 {
		t.Fatalf("expected entry pruned after kill, got %d entries", len(got))
	}
}

// TestReapMCPProxiesCountsSurvivorsAsFailed drives the escalation
// timeout leg: a gramaton proxy that ignores both SIGTERM and the
// kill escalation must come back in the failed count (uninstall
// turns survivors into a non-zero exit), keep its registry entry,
// and be warned about.
func TestReapMCPProxiesCountsSurvivorsAsFailed(t *testing.T) {
	// Shrink the grace periods so the doubled waitForExit timeout
	// costs milliseconds, not seconds.
	oldWait, oldStep := reapWait, reapPollStep
	reapWait, reapPollStep = 10*time.Millisecond, time.Millisecond
	t.Cleanup(func() { reapWait, reapPollStep = oldWait, oldStep })

	dir := t.TempDir()
	target := os.Getpid() // must be genuinely alive for ListMCPProxies
	entryPath := writeProxyEntry(t, dir, target)

	ops := procOps{
		alive:             func(int) bool { return true }, // survives everything
		looksLikeGramaton: func(pid int) bool { return pid == target },
		protected:         func(int) bool { return false },
		terminate:         func(int) error { return nil },
		kill:              func(int) error { return nil },
	}

	var warnings bytes.Buffer
	stopped, failed := reapMCPProxies(dir, ops, &warnings)
	if stopped != 0 {
		t.Errorf("stopped = %d, want 0", stopped)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1 (the survivor must be counted)", failed)
	}
	if !strings.Contains(warnings.String(), "did not exit") {
		t.Errorf("warning should name the surviving proxy: %q", warnings.String())
	}
	if _, err := os.Stat(entryPath); err != nil {
		t.Errorf("registry entry should remain for a surviving proxy: %v", err)
	}
}

// TestReapRealProcess exercises the reaper against a real child
// process with the production signal path: SIGTERM, liveness poll,
// entry pruning. The name check is stubbed to approve the child
// (it runs sleep, not gramaton).
func TestReapRealProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sleep(1) and SIGTERM; the Windows path is Process.Kill, covered by stubs")
	}

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	// Reap the child as soon as it dies. Without this the killed
	// child stays a zombie (we are its parent), and zombies still
	// pass the signal-0 liveness probe, so waitForExit would never
	// see it exit.
	waitDone := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waitDone) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-waitDone })

	dir := t.TempDir()
	entryPath := writeProxyEntry(t, dir, pid)

	ops := defaultProcOps(dir)
	ops.looksLikeGramaton = func(p int) bool { return p == pid }

	var warnings bytes.Buffer
	stopped, _ := reapMCPProxies(dir, ops, &warnings)

	if stopped != 1 {
		t.Fatalf("stopped = %d, want 1 (warnings: %q)", stopped, warnings.String())
	}
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("child still running after reap")
	}
	if _, err := os.Stat(entryPath); !os.IsNotExist(err) {
		t.Fatal("registry entry should be pruned after the kill")
	}
}

// TestRunStopKeepMCP verifies the --keep-mcp opt-out: with it, stop
// leaves the registry untouched; without it, the reap pass runs
// (observable here as the dead entry being pruned) before the server
// shutdown is attempted. Both runs use a config dir with no
// server.json, so stopServer fails with "no running server" and
// nothing real is signalled.
func TestRunStopKeepMCP(t *testing.T) {
	t.Run("keep-mcp skips the reap", func(t *testing.T) {
		dir := t.TempDir()
		path := writeProxyEntry(t, dir, deadPID)

		err := runStop(dir, true)
		if err == nil {
			t.Fatal("expected no-running-server error")
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("entry should be intact with --keep-mcp: %v", statErr)
		}
	})

	t.Run("default reaps before stopping the server", func(t *testing.T) {
		dir := t.TempDir()
		path := writeProxyEntry(t, dir, deadPID)

		err := runStop(dir, false)
		if err == nil {
			t.Fatal("expected no-running-server error")
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatal("dead entry should be pruned by the reap pass")
		}
	})
}

// TestCLIStatusListsMCPProxies goes through the real status command
// against the shared integration server: a registered live proxy
// appears in the mcp_proxies field, a dead entry does not.
func TestCLIStatusListsMCPProxies(t *testing.T) {
	if err := server.RegisterMCPProxy(testCfgDir); err != nil {
		t.Fatalf("RegisterMCPProxy: %v", err)
	}
	t.Cleanup(func() { server.RemoveMCPProxy(testCfgDir, os.Getpid()) })
	deadPath := writeProxyEntry(t, testCfgDir, deadPID)

	out, err := runCmd(t, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	result := parseOutput(t, out)
	field, ok := result["mcp_proxies"].(map[string]any)
	if !ok {
		t.Fatalf("expected mcp_proxies object: %s", string(out))
	}
	if count, _ := field["count"].(float64); count != 1 {
		t.Fatalf("mcp_proxies.count = %v, want 1: %s", field["count"], string(out))
	}
	proxies, ok := field["proxies"].([]any)
	if !ok || len(proxies) != 1 {
		t.Fatalf("expected 1 proxy entry: %s", string(out))
	}
	entry := proxies[0].(map[string]any)
	if pid, _ := entry["pid"].(float64); int(pid) != os.Getpid() {
		t.Errorf("proxy pid = %v, want %d", entry["pid"], os.Getpid())
	}
	if entry["started_at"] == nil {
		t.Error("proxy entry should carry started_at")
	}
	// The dead entry was pruned during the listing, not reported.
	if _, statErr := os.Stat(deadPath); !os.IsNotExist(statErr) {
		t.Error("dead entry should be pruned by status listing")
	}
}

// The reaper must never signal its own process, even when a registry
// entry lands on its PID and the inspection stubs approve it (PID
// recycled onto the stop process itself). The entry is skipped with
// a warning and left in place.
func TestReapNeverSignalsSelf(t *testing.T) {
	dir := t.TempDir()
	path := writeProxyEntry(t, dir, os.Getpid())

	var calls []string
	ops := procOps{
		alive:             func(int) bool { return true },
		looksLikeGramaton: func(int) bool { return true },
		protected:         defaultProcOps(dir).protected,
		terminate: func(pid int) error {
			calls = append(calls, fmt.Sprintf("terminate:%d", pid))
			return nil
		},
		kill: func(pid int) error {
			calls = append(calls, fmt.Sprintf("kill:%d", pid))
			return nil
		},
	}

	var warnings bytes.Buffer
	if stopped, _ := reapMCPProxies(dir, ops, &warnings); stopped != 0 {
		t.Fatalf("stopped = %d, want 0", stopped)
	}
	if len(calls) != 0 {
		t.Fatalf("self PID was signalled: %v", calls)
	}
	if !bytes.Contains(warnings.Bytes(), []byte("stop or server process")) {
		t.Errorf("expected self-PID warning, got %q", warnings.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("self entry should remain for List to collect later: %v", err)
	}
}

// processLooksLikeGramaton must fail closed: inspection failures and
// non-gramaton owners both answer false.
func TestProcessLooksLikeGramatonFailsClosed(t *testing.T) {
	if processLooksLikeGramaton(deadPID) {
		t.Error("unknown/dead pid must not look like gramaton (ps errors fail closed)")
	}
	// The test binary is cli.test, not gramaton.
	if processLooksLikeGramaton(os.Getpid()) {
		t.Error("the test binary must not look like gramaton")
	}
}

// The default protection closure shields the reaper's own PID and a
// registered live server's PID, and nothing else.
func TestDefaultProcOpsProtects(t *testing.T) {
	dir := t.TempDir()
	ops := defaultProcOps(dir)
	if !ops.protected(os.Getpid()) {
		t.Error("own PID must be protected")
	}
	if ops.protected(deadPID) {
		t.Error("unrelated PID must not be protected")
	}
}
