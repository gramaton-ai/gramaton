package api

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs every test in the api package under goleak's
// VerifyNone, so a leaked goroutine in any L5 async runner test (or
// future async test) fails the suite immediately rather than
// silently leaking.
//
// Known long-lived goroutines that aren't leaks: the
// preparedSweeper started by api.New is stopped via t.Cleanup in
// setupTestAPI/setupBatchAPI (see sessions_test.go); the async
// runner registry is drained by ShutdownAsync (also in t.Cleanup).
// If a future test introduces a real leak goleak's report will name
// the leaking stack so the failure is actionable.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// goleak's default ignores cover the testing framework
		// itself. We add nothing here yet -- if a stable
		// known-good goroutine emerges, IgnoreTopFunction is the
		// right tool.
	)
}
