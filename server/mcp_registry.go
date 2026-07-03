package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/core"
)

// MCPProxyInfo describes a registered `gramaton mcp` proxy process.
// It is written to <config-dir>/mcp/<pid>.json when a proxy starts
// and removed on clean exit. Proxies are spawned by MCP clients (not
// by the server), so this registry is the only way the rest of the
// CLI can discover them: `gramaton stop` reaps registered proxies
// and `gramaton status` lists them.
type MCPProxyInfo struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	// Binary is the resolved executable path of the proxy process,
	// from os.Executable(). Surfaced by `gramaton status` so a proxy
	// that predates a binary upgrade is visible. Gramaton never
	// restarts proxies -- the spawning harness owns respawning.
	Binary string `json:"binary,omitempty"`
}

// mcpRegistryDir returns the proxy registry directory inside a
// store's config dir, sibling to server.json.
func mcpRegistryDir(cfgDir string) string {
	return filepath.Join(cfgDir, "mcp")
}

// mcpProxyPath returns the registration file path for a proxy PID.
func mcpProxyPath(cfgDir string, pid int) string {
	return filepath.Join(mcpRegistryDir(cfgDir), fmt.Sprintf("%d.json", pid))
}

// RegisterMCPProxy writes a registration file for the current
// process into the store's config dir. Callers treat failure as
// non-fatal bookkeeping: a proxy that cannot register still serves
// tool calls.
func RegisterMCPProxy(cfgDir string) error {
	binary, err := os.Executable()
	if err != nil {
		binary = "" // best-effort; the PID is the load-bearing field
	}
	info := MCPProxyInfo{
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC(),
		Binary:    binary,
	}

	if err := os.MkdirAll(mcpRegistryDir(cfgDir), 0o700); err != nil {
		return fmt.Errorf("create mcp registry dir: %w", err)
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mcp proxy info: %w", err)
	}
	return core.AtomicWriteFile(mcpProxyPath(cfgDir, info.PID), data, 0o600)
}

// RemoveMCPProxy deletes the registration file for a proxy PID.
// Removing a missing file is a no-op: SIGKILLed proxies never get to
// clean up, and ListMCPProxies may already have pruned the entry.
func RemoveMCPProxy(cfgDir string, pid int) {
	_ = os.Remove(mcpProxyPath(cfgDir, pid))
}

// ListMCPProxies returns the registered proxies whose PID is still
// alive, sorted by PID. Entries that are unparseable or refer to a
// dead process are pruned from disk as the list is read: a proxy
// killed without cleanup (SIGKILL, harness crash) leaves a stale
// file behind, and this is where it gets collected.
//
// Liveness is a PID-existence probe only. A PID recycled by an
// unrelated process passes this check; callers that act on entries
// (notably the stop command's reaper) must verify the process
// identity before signalling it.
func ListMCPProxies(cfgDir string) []MCPProxyInfo {
	return listMCPProxies(cfgDir, true)
}

// ListMCPProxiesNoPrune is the read-only variant of ListMCPProxies:
// the same live-proxy filtering, but stale or unparseable entries
// are left on disk instead of being collected. For inventory-style
// callers (uninstall's dry-run and pre-confirmation survey) that
// must not mutate anything on disk.
func ListMCPProxiesNoPrune(cfgDir string) []MCPProxyInfo {
	return listMCPProxies(cfgDir, false)
}

func listMCPProxies(cfgDir string, prune bool) []MCPProxyInfo {
	entries, err := os.ReadDir(mcpRegistryDir(cfgDir))
	if err != nil {
		return nil // no registry dir yet: no proxies ever registered
	}

	var out []MCPProxyInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(mcpRegistryDir(cfgDir), e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var info MCPProxyInfo
		if err := json.Unmarshal(data, &info); err != nil || info.PID <= 0 {
			if prune {
				_ = os.Remove(path)
			}
			continue
		}
		if !IsProcessAlive(info.PID) {
			if prune {
				_ = os.Remove(path)
			}
			continue
		}
		out = append(out, info)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}
