package curation

import (
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// TestCollectGarbageDeletesUnclassifiedDebris is the load-bearing
// regression for the GC unclassified-debris fix. Pre-fix, collectGarbage required the
// record to have temporality=="ephemeral", which is set by LLM
// classification. But the captured-status filter immediately above
// requires the record to be UNCLASSIFIED, so temporality is unset.
// The two requirements were contradictory and made GC essentially a
// no-op for unclassified debris (the exact records GC is meant to
// clean up).
//
// Post-fix: temporality unset OR ephemeral both pass. This test
// builds an aged, untouched, unclassified record with all the GC
// preconditions met EXCEPT the old ephemeral requirement, and
// asserts it gets deleted.
func TestCollectGarbageDeletesUnclassifiedDebris(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.GC.Enabled = true
	cfg.GC.DryRun = false
	cfg.GC.MinAgeDays = 30

	old := time.Now().UTC().AddDate(0, 0, -60) // well past minAge

	eng.Lock()
	debris := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("aged debris record never classified"),
		"processing_status": graph.StringProperty("captured"),
		"created_at":        graph.TimestampProperty(old),
		"access_count":      graph.Int64Property(0),
		// No confidence, no importance, no temporality (unclassified).
	})
	for k, v := range debris.Properties {
		eng.PropIdx().Add(debris.ID, k, v)
	}
	eng.Save("seed-debris")
	eng.Unlock()

	deleted := collectGarbage(eng, cfg, nil)
	if deleted != 1 {
		t.Errorf("expected 1 deletion, got %d", deleted)
	}

	eng.RLock()
	defer eng.RUnlock()
	if _, ok := eng.Graph().GetNode(debris.ID); ok {
		t.Errorf("debris record %s should have been deleted", debris.ID)
	}
}

// TestCollectGarbageStillDeletesEphemeralDebris confirms the
// ephemeral path still works post-fix (the relaxation widens the
// criterion from "ephemeral only" to "ephemeral or unset").
func TestCollectGarbageStillDeletesEphemeralDebris(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.GC.Enabled = true
	cfg.GC.DryRun = false
	cfg.GC.MinAgeDays = 30

	old := time.Now().UTC().AddDate(0, 0, -60)

	eng.Lock()
	debris := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("aged ephemeral debris"),
		"processing_status": graph.StringProperty("captured"),
		"temporality":       graph.StringProperty("ephemeral"),
		"created_at":        graph.TimestampProperty(old),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range debris.Properties {
		eng.PropIdx().Add(debris.ID, k, v)
	}
	eng.Save("seed-ephemeral")
	eng.Unlock()

	deleted := collectGarbage(eng, cfg, nil)
	if deleted != 1 {
		t.Errorf("expected 1 deletion, got %d", deleted)
	}
}

// TestCollectGarbageRespectsDurableTemporality confirms records
// the LLM classified as durable (or temporal) are NOT deleted —
// only unset and ephemeral pass the relaxed gate.
func TestCollectGarbageRespectsDurableTemporality(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.GC.Enabled = true
	cfg.GC.DryRun = false
	cfg.GC.MinAgeDays = 30

	old := time.Now().UTC().AddDate(0, 0, -60)

	cases := []struct {
		name string
		temp string
	}{
		{"durable", "durable"},
		{"temporal", "temporal"},
		{"immutable", "immutable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := setupEngine(t)
			cfg := eng.Config()
			cfg.GC.Enabled = true
			cfg.GC.DryRun = false
			cfg.GC.MinAgeDays = 30

			eng.Lock()
			n := eng.Graph().AddNode(graph.Properties{
				"content_full":      graph.StringProperty("aged but classified " + tc.temp),
				"processing_status": graph.StringProperty("captured"),
				"temporality":       graph.StringProperty(tc.temp),
				"created_at":        graph.TimestampProperty(old),
				"access_count":      graph.Int64Property(0),
			})
			for k, v := range n.Properties {
				eng.PropIdx().Add(n.ID, k, v)
			}
			eng.Save("seed-" + tc.name)
			eng.Unlock()

			deleted := collectGarbage(eng, cfg, nil)
			if deleted != 0 {
				t.Errorf("temporality=%q should NOT be GC-eligible; got %d deletions", tc.temp, deleted)
			}
		})
	}
}

// TestCollectGarbageRemovesAccessSidecarEntry pins deletion hygiene
// for the GC path. Access bookkeeping lives in its own bbolt file
// outside the commit substrate, so removing the node and its derived
// indexes reclaims nothing there: an entry left behind survives
// backup/restore and re-overlays onto the ID if the record is ever
// re-imported.
func TestCollectGarbageRemovesAccessSidecarEntry(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.GC.Enabled = true
	cfg.GC.DryRun = false
	cfg.GC.MinAgeDays = 30

	old := time.Now().UTC().AddDate(0, 0, -60)

	eng.Lock()
	debris := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("aged debris that burned embed attempts"),
		"processing_status": graph.StringProperty("captured"),
		"created_at":        graph.TimestampProperty(old),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range debris.Properties {
		eng.PropIdx().Add(debris.ID, k, v)
	}
	eng.Save("seed-debris")
	// Failed embed attempts are the sidecar writer that leaves a
	// record GC-eligible: they bump no access count.
	eng.SetEmbedAttempts(debris.ID, 3)
	eng.Unlock()

	if _, ok := eng.AccessIdx().Get(debris.ID); !ok {
		t.Fatal("sidecar entry missing before GC; the removal assertion would be vacuous")
	}

	if deleted := collectGarbage(eng, cfg, nil); deleted != 1 {
		t.Fatalf("expected 1 deletion, got %d", deleted)
	}

	if m, ok := eng.AccessIdx().Get(debris.ID); ok {
		t.Fatalf("sidecar entry %+v survived the GC deletion", m)
	}
}

// TestCollectGarbageRespectsAgeFloor confirms a young unclassified
// record is preserved even when all other GC criteria pass. The
// age check is what gives users a chance to act on captured data
// before deletion fires.
func TestCollectGarbageRespectsAgeFloor(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.GC.Enabled = true
	cfg.GC.DryRun = false
	cfg.GC.MinAgeDays = 30

	young := time.Now().UTC().AddDate(0, 0, -1) // 1 day old

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("young unclassified record"),
		"processing_status": graph.StringProperty("captured"),
		"created_at":        graph.TimestampProperty(young),
		"access_count":      graph.Int64Property(0),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("seed-young")
	eng.Unlock()

	deleted := collectGarbage(eng, cfg, nil)
	if deleted != 0 {
		t.Errorf("young record should not be GC'd, got %d deletions", deleted)
	}
}

// TestCollectGarbageRespectsAccessCount confirms an aged record with
// any access_count > 0 is preserved (someone touched it; treat as
// signal of value).
func TestCollectGarbageRespectsAccessCount(t *testing.T) {
	eng := setupEngine(t)
	cfg := eng.Config()
	cfg.GC.Enabled = true
	cfg.GC.DryRun = false
	cfg.GC.MinAgeDays = 30

	old := time.Now().UTC().AddDate(0, 0, -60)

	eng.Lock()
	n := eng.Graph().AddNode(graph.Properties{
		"content_full":      graph.StringProperty("touched aged record"),
		"processing_status": graph.StringProperty("captured"),
		"created_at":        graph.TimestampProperty(old),
		"access_count":      graph.Int64Property(1),
	})
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	eng.Save("seed-touched")
	eng.Unlock()

	deleted := collectGarbage(eng, cfg, nil)
	if deleted != 0 {
		t.Errorf("touched record (access_count > 0) should not be GC'd, got %d", deleted)
	}
}
