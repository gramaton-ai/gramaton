package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/internal/setup"
)

// TestServerURLNoAutostartSuppression pins the receiving half of the
// uninstall resurrection fix: with GRAMATON_NO_AUTOSTART=1 and no
// server running, serverURL must refuse to auto-start -- before the
// config-driven auto_start toggle gets a say -- and name the
// variable in its error. The control leg (variable unset) proves the
// suppression error really came from the env check: it falls through
// to the config toggle instead.
//
// The fixture pins server.auto_start=false as a safety net, so even
// a fully regressed check could never spawn a real server out of the
// test binary; the falsifiable signal is the error text -- deleting
// the env check makes leg 1 return the auto_start=false message.
func TestServerURLNoAutostartSuppression(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows: os.UserHomeDir reads %USERPROFILE%, not $HOME
	t.Setenv("GRAMATON_STORE", "")

	dir := filepath.Join(home, ".gramaton")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("server:\n  auto_start: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	origCfgDir := cfgDir
	cfgDir = dir
	t.Cleanup(func() { cfgDir = origCfgDir })

	// Leg 1: suppression set -- the env check must answer, not the
	// config toggle, and nothing may be started.
	t.Setenv(setup.NoAutostartEnv, "1")
	_, err := serverURL()
	if err == nil {
		t.Fatal("serverURL should refuse with no server and suppressed auto-start")
	}
	if !strings.Contains(err.Error(), setup.NoAutostartEnv) {
		t.Errorf("error should name %s (the suppression, not the config toggle): %v", setup.NoAutostartEnv, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "server.json")); !os.IsNotExist(statErr) {
		t.Error("no server may be auto-started under suppression")
	}

	// Leg 2 (control): variable unset (empty) -- existing behavior:
	// the config-driven auto_start toggle answers.
	t.Setenv(setup.NoAutostartEnv, "")
	_, err = serverURL()
	if err == nil {
		t.Fatal("serverURL should still refuse (auto_start=false)")
	}
	if !strings.Contains(err.Error(), "auto_start=false") {
		t.Errorf("control leg should hit the config toggle: %v", err)
	}
	if strings.Contains(err.Error(), setup.NoAutostartEnv) {
		t.Errorf("an unset variable must not trigger the suppression path: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "server.json")); !os.IsNotExist(statErr) {
		t.Error("control leg must not start a server either")
	}
}

// TestWaitForServerBailsWhenChildExits pins the fast-fail path: a
// closed childExited channel (the auto-started server died, e.g. a
// config-load failure) returns immediately instead of spinning out
// the full timeout probing a server that will never come up.
func TestWaitForServerBailsWhenChildExits(t *testing.T) {
	dir := t.TempDir()
	exited := make(chan struct{})
	close(exited)

	start := time.Now()
	err := waitForServer(dir, 10*time.Second, exited)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when child already exited")
	}
	if !strings.Contains(err.Error(), "exited during startup") {
		t.Errorf("error should name the child exit: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("should bail immediately, took %s", elapsed)
	}

	// A nil channel never fires: the poll loop runs to its deadline
	// as before (short timeout keeps the test fast).
	start = time.Now()
	err = waitForServer(dir, 300*time.Millisecond, nil)
	if err == nil {
		t.Fatal("expected timeout error with no server")
	}
	if strings.Contains(err.Error(), "exited during startup") {
		t.Errorf("nil channel must not report a child exit: %v", err)
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Error("nil-channel leg should poll to the deadline")
	}
}
