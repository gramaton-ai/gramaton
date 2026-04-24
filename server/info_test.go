package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gramaton-ai/gramaton/internal/version"
)

func TestWriteAndReadServerInfo(t *testing.T) {
	srv, _ := setupTestServer(t)

	if err := srv.writeServerInfo(); err != nil {
		t.Fatalf("writeServerInfo: %v", err)
	}

	// Verify file exists.
	path := srv.serverInfoPath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("server.json not found: %v", err)
	}

	// Read it back.
	info, err := ReadServerInfo(srv.cfg.ConfigDir)
	if err != nil {
		t.Fatalf("ReadServerInfo: %v", err)
	}

	if info.PID != os.Getpid() {
		t.Fatalf("expected PID %d, got %d", os.Getpid(), info.PID)
	}
	if info.Port != srv.cfg.Port {
		t.Fatalf("expected port %d, got %d", srv.cfg.Port, info.Port)
	}
	if info.Version != version.Version {
		t.Fatalf("expected version %q, got %q", version.Version, info.Version)
	}
}

func TestRemoveServerInfo(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.writeServerInfo()

	srv.removeServerInfo()

	if _, err := os.Stat(srv.serverInfoPath()); !os.IsNotExist(err) {
		t.Fatal("server.json should be removed")
	}
}

func TestReadServerInfoNotFound(t *testing.T) {
	_, err := ReadServerInfo(t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing server.json")
	}
}

func TestReadServerInfoInvalid(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "server.json"), []byte("not json"), 0o600)

	_, err := ReadServerInfo(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestRemoveServerInfoStatic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.json")
	os.WriteFile(path, []byte("{}"), 0o600)

	RemoveServerInfo(dir)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("server.json should be removed")
	}
}

func TestIsProcessAlive(t *testing.T) {
	// Current process should be alive.
	if !IsProcessAlive(os.Getpid()) {
		t.Fatal("current process should be alive")
	}

	// PID 0 is special (kernel), should not appear alive to us.
	// PID -1 should not be alive.
	if IsProcessAlive(-1) {
		t.Fatal("PID -1 should not be alive")
	}
}

func TestRequestShutdownNonBlocking(t *testing.T) {
	// RequestShutdown must queue a reason on s.shutdownCh without
	// blocking, even under heavy concurrent pressure. Before the
	// channel-based refactor this went through os.FindProcess +
	// p.Signal(SIGTERM), which was non-blocking but unsupported on
	// Windows. The new implementation uses a buffered channel with
	// a non-blocking select-send so excess requests drop instead of
	// deadlocking.
	srv, _ := setupTestServer(t)

	for i := 0; i < 50; i++ {
		srv.RequestShutdown()
	}

	select {
	case reason := <-srv.shutdownCh:
		if reason != "api-request" {
			t.Errorf("shutdown reason = %q, want api-request", reason)
		}
	default:
		t.Fatal("expected at least one shutdown reason queued")
	}

	// After draining, subsequent RequestShutdown should still queue
	// one reason — the channel is ready to accept again.
	srv.RequestShutdown()
	select {
	case reason := <-srv.shutdownCh:
		if reason != "api-request" {
			t.Errorf("second shutdown reason = %q, want api-request", reason)
		}
	default:
		t.Fatal("expected second RequestShutdown to queue")
	}
}

func TestServerInfoPath(t *testing.T) {
	srv, _ := setupTestServer(t)
	path := srv.serverInfoPath()
	if filepath.Base(path) != "server.json" {
		t.Fatalf("expected server.json, got %q", filepath.Base(path))
	}
}
