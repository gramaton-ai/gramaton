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

	// A rolled-back batch must not stamp. Asserted on a FRESH engine
	// where no stamp exists at all: on the engine above the pre-batch
	// stamp already equals the would-be value, so a buggy write would
	// be invisible.
	eng2 := setupTestEngine(t)
	_ = eng2.WithWriteBatch("rollback pin", func(ws *WriteSession) (bool, error) {
		return true, errRollbackPin
	})
	if _, ok := eng2.indexes.pendingBatchHead(); ok {
		t.Fatal("rolled-back batch left a stamp; the tx never committed, so there is no divergence to report")
	}
}

var errRollbackPin = &probeTestError{}

type probeTestError struct{}

func (e *probeTestError) Error() string { return "deliberate rollback" }
