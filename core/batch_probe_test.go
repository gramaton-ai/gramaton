package core

import (
	"testing"

	bolt "go.etcd.io/bbolt"
)

// TestUnsavedBatchProbe pins the two-fsync-window boot probe. A
// write batch commits its index transaction and THEN mints the graph
// commit; a crash between the two leaves the index DB ahead of the
// graph with nothing recording the divergence. The batch tx stamps
// the pre-batch HEAD; a healthy Save moves HEAD off the stamp, so a
// boot that finds stamp == HEAD knows the last batch's graph commit
// never landed.
func TestUnsavedBatchProbe(t *testing.T) {
	eng := setupTestEngine(t)

	if eng.UnsavedBatchDetected() {
		t.Fatal("fresh store must not report an unsaved batch")
	}

	// A healthy mutating batch: stamp written inside the tx, Save
	// lands, HEAD moves off the stamp.
	err := eng.WithWriteBatch("probe pin", func(ws *WriteSession) (bool, error) {
		return true, nil
	})
	if err != nil {
		t.Fatalf("WithWriteBatch: %v", err)
	}
	if _, ok := eng.indexes.pendingBatchHead(); !ok {
		t.Fatal("mutating batch left no stamp")
	}
	if eng.UnsavedBatchDetected() {
		t.Fatal("healthy batch (Save landed) must not trip the probe")
	}

	// Simulate the crash window: the stamp equals HEAD because the
	// Save that should have moved HEAD never happened.
	uerr := eng.indexes.boltDB.Update(func(tx *bolt.Tx) error {
		return eng.indexes.stampPendingBatchHeadTx(tx, eng.HeadHash())
	})
	if uerr != nil {
		t.Fatalf("stamp: %v", uerr)
	}
	if !eng.UnsavedBatchDetected() {
		t.Fatal("stamp == HEAD must report the unsaved batch")
	}

	// A rolled-back batch must not stamp: the tx never committed, so
	// there is no divergence to report.
	preStamp, _ := eng.indexes.pendingBatchHead()
	_ = eng.WithWriteBatch("rollback pin", func(ws *WriteSession) (bool, error) {
		return true, errRollbackPin
	})
	if got, _ := eng.indexes.pendingBatchHead(); got != preStamp {
		t.Fatalf("rolled-back batch changed the stamp: %q -> %q", preStamp, got)
	}
}

var errRollbackPin = &probeTestError{}

type probeTestError struct{}

func (e *probeTestError) Error() string { return "deliberate rollback" }
