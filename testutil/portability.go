package testutil

import (
	"os"
	"runtime"
	"testing"
	"time"
)

// Timeout scales a base test timeout for the host platform. Windows
// runners (especially under race detector) are 3-5x slower than Linux
// or macOS for I/O-heavy paths, and many of our async tests use
// short hard-coded timeouts (1-5s) that cleanly fit POSIX timing but
// fail on Windows CI through no fault of the code under test.
//
// Use this when a test waits on an event with a deadline:
//
//	pollUntilTerminal(t, a, jobID, testutil.Timeout(5*time.Second))
//
// On Linux/macOS this returns base unchanged. On Windows it returns
// 3 * base. The multiplier is conservative: Windows under race can
// be ~5x slower in practice, but a tighter multiplier surfaces
// genuine deadlocks faster while still clearing flakes.
func Timeout(base time.Duration) time.Duration {
	if runtime.GOOS == "windows" {
		return base * 3
	}
	return base
}

// AssertFileMode verifies a regular file's mode matches `want` on
// POSIX systems. On Windows the mode-bit assertion is skipped
// (Windows reports a fixed 0o666 / 0o444 based on the read-only
// flag and does not honor full POSIX mode bits) but the file's
// existence + non-directory-ness is still verified so callers
// retain a basic correctness check.
//
// Use this in place of direct `info.Mode().Perm() != 0o600` checks
// when the test runs on every platform.
func AssertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.IsDir() {
		t.Errorf("%s: expected file, got directory", path)
		return
	}
	if runtime.GOOS == "windows" {
		return
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s: expected mode %o, got %o", path, want, got)
	}
}

// AssertDirMode is AssertFileMode for directories. Same Windows
// caveat: the mode-bit assertion is skipped on Windows but the
// path is verified to exist and to be a directory.
func AssertDirMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !info.IsDir() {
		t.Errorf("%s: expected directory, got file", path)
		return
	}
	if runtime.GOOS == "windows" {
		return
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s: expected mode %o, got %o", path, want, got)
	}
}
