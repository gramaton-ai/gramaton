package setup

import (
	"bufio"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// Cleanup-stack tests. The stack is a small amount of code but a
// critical contract: if the wizard's interrupt-handling or error-
// rollback misses a cleanup, users get orphan files on disk.

// TestCleanupsRunLIFOOnRollback verifies that Wizard.runCleanups
// executes registered cleanups in reverse-registration order and
// clears the list after running (calling again is a no-op).
func TestCleanupsRunLIFOOnRollback(t *testing.T) {
	w := &Wizard{}

	var order []int
	w.addCleanup(func() { order = append(order, 1) })
	w.addCleanup(func() { order = append(order, 2) })
	w.addCleanup(func() { order = append(order, 3) })

	w.runCleanups()

	if len(order) != 3 {
		t.Fatalf("want 3 cleanups run, got %d", len(order))
	}
	// LIFO: last registered runs first.
	if order[0] != 3 || order[1] != 2 || order[2] != 1 {
		t.Errorf("cleanup order LIFO expected [3,2,1], got %v", order)
	}

	// Second call must be a no-op (idempotent).
	w.runCleanups()
	if len(order) != 3 {
		t.Errorf("runCleanups not idempotent: ran again, len now %d", len(order))
	}
}

// TestMarkCommittedDiscardsCleanups verifies the success path: once
// the wizard commits, the cleanup list is cleared without running.
// A subsequent runCleanups call (e.g., from the deferred handler)
// must be a no-op; otherwise we'd tear down the user's intended
// final state.
func TestMarkCommittedDiscardsCleanups(t *testing.T) {
	w := &Wizard{}

	var ran atomic.Int32
	w.addCleanup(func() { ran.Add(1) })
	w.addCleanup(func() { ran.Add(1) })

	w.markCommitted()
	if !w.committed {
		t.Error("markCommitted did not set committed flag")
	}

	// Subsequent runCleanups must not execute the discarded entries.
	w.runCleanups()
	if ran.Load() != 0 {
		t.Errorf("cleanups ran after commit: %d", ran.Load())
	}
}

// TestWizardRunClearsCleanupsOnSuccess is the integration test: a
// successful wizard run (fresh path, skip everything) must not leave
// any registered cleanups hanging. We can't easily assert "no
// cleanups ran" from the public API, so we register a probe cleanup
// manually after construction and verify markCommitted wiped it.
func TestWizardRunClearsCleanupsOnSuccess(t *testing.T) {
	// We don't actually Run() here -- just exercise the
	// markCommitted contract. The full-path wizard tests
	// (TestWizardFreshPathSkipEverything) exercise the end-to-end
	// flow; this focused test pins down the contract without
	// pulling in the whole fs/embed/network surface.
	w := &Wizard{}
	var triggered atomic.Bool
	w.addCleanup(func() { triggered.Store(true) })

	w.markCommitted()

	// Simulate the deferred rollback path in Run().
	w.runCleanups()

	if triggered.Load() {
		t.Error("cleanup ran after markCommitted; success path would have destroyed user state")
	}
}

// TestTextEnforcesLineCap verifies the paste-bomb guard. A line
// longer than maxLineBytes (8 KB) returns ErrInputTooLong instead
// of silently buffering multi-megabyte inputs.
func TestTextEnforcesLineCap(t *testing.T) {
	// 9 KB of 'a' followed by newline -- one byte past the cap.
	huge := strings.Repeat("a", maxLineBytes+1) + "\n"
	p := &TerminalPrompter{reader: bufio.NewReader(strings.NewReader(huge))}

	_, err := p.Text("")
	if !errors.Is(err, ErrInputTooLong) {
		t.Fatalf("want ErrInputTooLong, got %v", err)
	}
}

// TestTextAcceptsUpToCap ensures the cap is a ceiling, not a floor:
// a line at exactly maxLineBytes (including its newline) still
// works. Regression guard for off-by-one in the cap check.
func TestTextAcceptsUpToCap(t *testing.T) {
	// maxLineBytes is the total line length including newline.
	// A line of (maxLineBytes-1) 'a' + '\n' is exactly maxLineBytes.
	line := strings.Repeat("a", maxLineBytes-1) + "\n"
	p := &TerminalPrompter{reader: bufio.NewReader(strings.NewReader(line))}

	got, err := p.Text("")
	if err != nil {
		t.Fatalf("at-cap line errored: %v", err)
	}
	if len(got) != maxLineBytes-1 {
		t.Errorf("want %d chars, got %d", maxLineBytes-1, len(got))
	}
}
