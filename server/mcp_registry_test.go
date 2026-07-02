package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// deadTestPID is a PID that cannot refer to a live process on any
// supported platform: Linux caps PIDs at PID_MAX_LIMIT (4194304),
// macOS at 99998, and Windows PIDs stay far below this in practice.
const deadTestPID = 1 << 30

// writeProxyEntry writes a registry entry for an arbitrary PID,
// bypassing RegisterMCPProxy (which always registers the current
// process).
func writeProxyEntry(t *testing.T, cfgDir string, pid int) string {
	t.Helper()
	dir := mcpRegistryDir(cfgDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir registry: %v", err)
	}
	info := MCPProxyInfo{PID: pid, StartedAt: time.Now().UTC(), Binary: "/fake/gramaton"}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%d.json", pid))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	return path
}

func TestRegisterMCPProxyRoundTrip(t *testing.T) {
	dir := t.TempDir()

	if err := RegisterMCPProxy(dir); err != nil {
		t.Fatalf("RegisterMCPProxy: %v", err)
	}

	path := mcpProxyPath(dir, os.Getpid())
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("registration file not found: %v", err)
	}

	proxies := ListMCPProxies(dir)
	if len(proxies) != 1 {
		t.Fatalf("expected 1 proxy, got %d", len(proxies))
	}
	p := proxies[0]
	if p.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", p.PID, os.Getpid())
	}
	if p.StartedAt.IsZero() {
		t.Error("StartedAt should be set")
	}
	exe, err := os.Executable()
	if err == nil && p.Binary != exe {
		t.Errorf("Binary = %q, want %q", p.Binary, exe)
	}
}

func TestRemoveMCPProxy(t *testing.T) {
	dir := t.TempDir()
	if err := RegisterMCPProxy(dir); err != nil {
		t.Fatalf("RegisterMCPProxy: %v", err)
	}

	RemoveMCPProxy(dir, os.Getpid())

	if _, err := os.Stat(mcpProxyPath(dir, os.Getpid())); !os.IsNotExist(err) {
		t.Fatal("registration file should be removed")
	}
	if got := ListMCPProxies(dir); len(got) != 0 {
		t.Fatalf("expected no proxies after remove, got %d", len(got))
	}

	// Removing an already-removed entry is a no-op.
	RemoveMCPProxy(dir, os.Getpid())
}

func TestListMCPProxiesPrunesDeadEntries(t *testing.T) {
	dir := t.TempDir()

	deadPath := writeProxyEntry(t, dir, deadTestPID)
	writeProxyEntry(t, dir, os.Getpid()) // alive: the test process itself

	proxies := ListMCPProxies(dir)
	if len(proxies) != 1 {
		t.Fatalf("expected 1 live proxy, got %d", len(proxies))
	}
	if proxies[0].PID != os.Getpid() {
		t.Errorf("surviving PID = %d, want %d", proxies[0].PID, os.Getpid())
	}
	if _, err := os.Stat(deadPath); !os.IsNotExist(err) {
		t.Fatal("dead-PID entry should be pruned from disk")
	}
}

func TestListMCPProxiesPrunesUnparseableEntries(t *testing.T) {
	dir := t.TempDir()
	regDir := mcpRegistryDir(dir)
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatalf("mkdir registry: %v", err)
	}

	junkPath := filepath.Join(regDir, "999.json")
	if err := os.WriteFile(junkPath, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write junk: %v", err)
	}
	// Non-JSON files are not registry entries and must be left alone.
	otherPath := filepath.Join(regDir, "README.txt")
	if err := os.WriteFile(otherPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write other: %v", err)
	}

	if got := ListMCPProxies(dir); len(got) != 0 {
		t.Fatalf("expected no proxies, got %d", len(got))
	}
	if _, err := os.Stat(junkPath); !os.IsNotExist(err) {
		t.Fatal("unparseable entry should be pruned")
	}
	if _, err := os.Stat(otherPath); err != nil {
		t.Fatalf("non-json file should be untouched: %v", err)
	}
}

func TestListMCPProxiesNoRegistryDir(t *testing.T) {
	if got := ListMCPProxies(t.TempDir()); got != nil {
		t.Fatalf("expected nil for missing registry dir, got %v", got)
	}
}
