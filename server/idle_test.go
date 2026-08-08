package server

import (
	"os"
	"testing"
)

// TestAttachedMCPProxies pins the idle watcher's proxy gate: a live
// registered proxy counts, deregistering drops the count, and a
// server constructed without a config dir always sees zero (the
// pre-registry behavior tests rely on).
func TestAttachedMCPProxies(t *testing.T) {
	dir := t.TempDir()
	s := &Server{cfg: Config{ConfigDir: dir}}

	if n := s.attachedMCPProxies(); n != 0 {
		t.Fatalf("empty registry: want 0, got %d", n)
	}

	// Register the current process -- alive by definition, so the
	// PID-liveness filter keeps it.
	if err := RegisterMCPProxy(dir); err != nil {
		t.Fatalf("register: %v", err)
	}
	if n := s.attachedMCPProxies(); n != 1 {
		t.Fatalf("live proxy: want 1, got %d", n)
	}

	RemoveMCPProxy(dir, os.Getpid())
	if n := s.attachedMCPProxies(); n != 0 {
		t.Fatalf("after remove: want 0, got %d", n)
	}

	none := &Server{cfg: Config{}}
	if n := none.attachedMCPProxies(); n != 0 {
		t.Fatalf("no config dir: want 0, got %d", n)
	}
}
