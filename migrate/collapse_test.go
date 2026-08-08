package migrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/testutil"
)

// addRecord creates a knowledge record; superseded=true stamps the
// selection props (resolution + valid_until).
func addRecord(t *testing.T, eng *core.Engine, short string, superseded bool) string {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()
	props := graph.Properties{
		"content_full":  graph.StringProperty(short + " full content"),
		"content_short": graph.StringProperty(short),
		"created_at":    graph.TimestampProperty(time.Now().UTC()),
	}
	if superseded {
		props["resolution"] = graph.StringProperty("superseded")
		props["valid_until"] = graph.TimestampProperty(time.Now().UTC().Add(-time.Hour))
	}
	n := eng.Graph().AddNode(props)
	for k, v := range n.Properties {
		eng.PropIdx().Add(n.ID, k, v)
	}
	return n.ID
}

func addSupersedes(t *testing.T, eng *core.Engine, successor, victim string, weight float64) {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()
	if _, err := eng.Graph().AddEdge(successor, victim, "supersedes", weight, nil); err != nil {
		t.Fatalf("AddEdge supersedes: %v", err)
	}
}

func save(t *testing.T, eng *core.Engine) {
	t.Helper()
	eng.Lock()
	defer eng.Unlock()
	if _, err := eng.Save("fixture"); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// TestBuildPlanSelectionConjunction pins the three-way selection: a
// props-passing victim collapses; a supersedes edge onto a record
// WITHOUT the props is a lineage anomaly, never a victim; superseded
// props with NO inbound edge is the swallowed-AddEdge anomaly.
func TestBuildPlanSelectionConjunction(t *testing.T) {
	eng := testutil.NewEngine(t)

	succ := addRecord(t, eng, "successor", false)
	victim := addRecord(t, eng, "victim", true)
	current := addRecord(t, eng, "current record with manual lineage", false)
	orphanProps := addRecord(t, eng, "superseded but edge-less", true)
	addSupersedes(t, eng, succ, victim, 0.95)
	addSupersedes(t, eng, succ, current, 0.95)
	save(t, eng)

	plan := BuildPlan(eng, DefaultPlanOptions())

	if len(plan.Victims) != 1 || plan.Victims[0].ID != victim {
		t.Fatalf("victims = %+v, want exactly %s", plan.Victims, victim)
	}
	if plan.Victims[0].SuccessorID != succ {
		t.Fatalf("successor = %s, want %s", plan.Victims[0].SuccessorID, succ)
	}
	if plan.TotalSupersedesEdges != 2 {
		t.Fatalf("TotalSupersedesEdges = %d, want 2", plan.TotalSupersedesEdges)
	}
	var lineage, orphan bool
	for _, a := range plan.Anomalies {
		switch a.Kind {
		case "lineage_edge_on_current":
			if a.NodeID == current {
				lineage = true
			}
		case "superseded_props_no_edge":
			if a.NodeID == orphanProps {
				orphan = true
			}
		}
	}
	if !lineage {
		t.Error("missing lineage_edge_on_current anomaly for the current-record edge")
	}
	if !orphan {
		t.Error("missing superseded_props_no_edge anomaly for the edge-less superseded record")
	}
}

// TestBuildPlanChainWalksToLiveHead pins successor resolution: in a
// chain A supersedes B supersedes C, both victims re-point to the
// live head A, not to each other.
func TestBuildPlanChainWalksToLiveHead(t *testing.T) {
	eng := testutil.NewEngine(t)

	a := addRecord(t, eng, "live head", false)
	b := addRecord(t, eng, "middle victim", true)
	c := addRecord(t, eng, "tail victim", true)
	addSupersedes(t, eng, a, b, 0.95)
	addSupersedes(t, eng, b, c, 0.94)
	save(t, eng)

	plan := BuildPlan(eng, DefaultPlanOptions())
	if len(plan.Victims) != 2 {
		t.Fatalf("victims = %d, want 2 (%+v)", len(plan.Victims), plan.Deferred)
	}
	for _, v := range plan.Victims {
		if v.SuccessorID != a {
			t.Errorf("victim %s successor = %s, want live head %s", v.ID, v.SuccessorID, a)
		}
	}
}

// TestBuildPlanMixedWeightPerVictim pins the per-victim eligibility
// the real stores demanded: eligibility is per victim (its own selection-edge
// weight), a below-floor victim defers, and the tail whose selection
// edge the collapse will cascade away is recorded as a stranded_tail
// anomaly for the props-keyed follow-up -- never silently dropped.
func TestBuildPlanMixedWeightPerVictim(t *testing.T) {
	eng := testutil.NewEngine(t)

	a := addRecord(t, eng, "head", false)
	b := addRecord(t, eng, "victim strong edge", true)
	c := addRecord(t, eng, "victim weak edge", true)
	addSupersedes(t, eng, a, b, 0.95)
	addSupersedes(t, eng, b, c, 0.60) // LLM-confidence-path weight
	save(t, eng)

	plan := BuildPlan(eng, DefaultPlanOptions())
	if len(plan.Victims) != 1 || plan.Victims[0].ID != b {
		t.Fatalf("victims = %+v, want only the strong-edge victim %s", plan.Victims, b)
	}
	if len(plan.Deferred) != 1 || plan.Deferred[0].VictimID != c {
		t.Fatalf("deferred = %+v, want the weak-edge victim %s", plan.Deferred, c)
	}
	if !strings.Contains(plan.Deferred[0].Reason, "below the 0.92 floor") {
		t.Errorf("deferral reason %q does not name the weight floor", plan.Deferred[0].Reason)
	}
	var stranded bool
	for _, an := range plan.Anomalies {
		if an.Kind == "stranded_tail" && an.NodeID == c {
			stranded = true
		}
	}
	if !stranded {
		t.Fatalf("deleting %s cascades %s's selection edge; expected a stranded_tail anomaly, got %+v", b, c, plan.Anomalies)
	}
}

// TestBuildPlanCollectionMismatchDefers pins the membership guard: a
// victim belonging to a collection the successor does not share
// defers to manual review; a co-member victim collapses.
func TestBuildPlanCollectionMismatchDefers(t *testing.T) {
	eng := testutil.NewEngine(t)

	succ := addRecord(t, eng, "successor", false)
	solo := addRecord(t, eng, "victim in unshared collection", true)
	comember := addRecord(t, eng, "victim sharing the collection", true)
	coll := addRecord(t, eng, "the collection container", false)
	eng.Lock()
	for _, src := range []string{solo, comember, succ} {
		if src == solo {
			continue
		}
		if _, err := eng.Graph().AddEdge(src, coll, "member_of", 1.0, nil); err != nil {
			eng.Unlock()
			t.Fatalf("AddEdge member_of: %v", err)
		}
	}
	coll2 := eng.Graph().AddNode(graph.Properties{"content_short": graph.StringProperty("unshared collection")})
	if _, err := eng.Graph().AddEdge(solo, coll2.ID, "member_of", 1.0, nil); err != nil {
		eng.Unlock()
		t.Fatalf("AddEdge member_of solo: %v", err)
	}
	eng.Unlock()
	addSupersedes(t, eng, succ, solo, 0.95)
	addSupersedes(t, eng, succ, comember, 0.95)
	save(t, eng)

	plan := BuildPlan(eng, DefaultPlanOptions())
	if len(plan.Victims) != 1 || plan.Victims[0].ID != comember {
		t.Fatalf("victims = %+v, want only the co-member victim", plan.Victims)
	}
	if len(plan.Deferred) != 1 || plan.Deferred[0].VictimID != solo {
		t.Fatalf("deferred = %+v, want the unshared-collection victim", plan.Deferred)
	}
}

// TestBuildPlanGathersProvenance pins that the plan carries the
// victim's segments (to re-point) and observation children (to
// cascade).
func TestBuildPlanGathersProvenance(t *testing.T) {
	eng := testutil.NewEngine(t)

	succ := addRecord(t, eng, "successor", false)
	victim := addRecord(t, eng, "victim with provenance", true)
	eng.Lock()
	seg := eng.Graph().AddNode(graph.Properties{
		"knowledge_type": graph.StringProperty("segment"),
		"content":        graph.StringProperty("the extracted conversation segment"),
	})
	obs := eng.Graph().AddNode(graph.Properties{
		"node_type":    graph.StringProperty("observation"),
		"content_full": graph.StringProperty("derived observation"),
	})
	if _, err := eng.Graph().AddEdge(seg.ID, victim, "extracted_as", 1.0, nil); err != nil {
		eng.Unlock()
		t.Fatalf("AddEdge extracted_as: %v", err)
	}
	if _, err := eng.Graph().AddEdge(obs.ID, victim, "observation_of", 1.0, nil); err != nil {
		eng.Unlock()
		t.Fatalf("AddEdge observation_of: %v", err)
	}
	eng.Unlock()
	addSupersedes(t, eng, succ, victim, 0.95)
	save(t, eng)

	plan := BuildPlan(eng, DefaultPlanOptions())
	if len(plan.Victims) != 1 {
		t.Fatalf("victims = %+v, want one", plan.Victims)
	}
	v := plan.Victims[0]
	if len(v.SegmentIDs) != 1 || v.SegmentIDs[0] != seg.ID {
		t.Errorf("SegmentIDs = %v, want [%s]", v.SegmentIDs, seg.ID)
	}
	if len(v.ObservationIDs) != 1 || v.ObservationIDs[0] != obs.ID {
		t.Errorf("ObservationIDs = %v, want [%s]", v.ObservationIDs, obs.ID)
	}
}

// TestBuildPlanForkCensusLiveOnly pins the report-only census: a live
// near-duplicate pair is reported; a pair with a historical side is
// not (it is either already in the plan or already resolved).
func TestBuildPlanForkCensusLiveOnly(t *testing.T) {
	eng := testutil.NewEngine(t)

	liveA := addRecord(t, eng, "live twin one", false)
	liveB := addRecord(t, eng, "live twin two", false)
	hist := addRecord(t, eng, "historical twin", true)
	vec := []float32{1, 0, 0, 0}
	eng.Lock()
	eng.Graph().SetNodeProperty(liveA, "embedding_full", graph.VectorProperty(vec))
	eng.Graph().SetNodeProperty(liveB, "embedding_full", graph.VectorProperty(vec))
	eng.Graph().SetNodeProperty(hist, "embedding_full", graph.VectorProperty(vec))
	eng.VecIdx().Add(liveA, vec)
	eng.VecIdx().Add(liveB, vec)
	eng.VecIdx().Add(hist, vec)
	eng.Unlock()
	save(t, eng)

	plan := BuildPlan(eng, DefaultPlanOptions())
	for _, p := range plan.ForkPairs {
		if p.IDA == hist || p.IDB == hist {
			t.Fatalf("fork census included the historical record: %+v", p)
		}
	}
	found := false
	for _, p := range plan.ForkPairs {
		if (p.IDA == liveA && p.IDB == liveB) || (p.IDA == liveB && p.IDB == liveA) {
			found = true
		}
	}
	if !found {
		t.Fatalf("fork census missed the live twin pair: %+v", plan.ForkPairs)
	}
}

// TestApplyCollapsesAndArchives drives the full apply: the victim is
// archived then deleted with its indexes cleaned, provenance
// re-points to the successor, the observation child cascades, and
// the survivor is untouched.
func TestApplyCollapsesAndArchives(t *testing.T) {
	eng := testutil.NewEngine(t)

	succ := addRecord(t, eng, "successor", false)
	victim := addRecord(t, eng, "victim", true)
	eng.Lock()
	seg := eng.Graph().AddNode(graph.Properties{
		"knowledge_type": graph.StringProperty("segment"),
		"captured_as":    graph.StringProperty(victim),
	})
	obs := eng.Graph().AddNode(graph.Properties{
		"node_type":    graph.StringProperty("observation"),
		"content_full": graph.StringProperty("derived observation"),
	})
	if _, err := eng.Graph().AddEdge(seg.ID, victim, "extracted_as", 1.0, nil); err != nil {
		eng.Unlock()
		t.Fatalf("AddEdge extracted_as: %v", err)
	}
	if _, err := eng.Graph().AddEdge(obs.ID, victim, "observation_of", 1.0, nil); err != nil {
		eng.Unlock()
		t.Fatalf("AddEdge observation_of: %v", err)
	}
	eng.Unlock()
	addSupersedes(t, eng, succ, victim, 0.95)
	save(t, eng)

	plan := BuildPlan(eng, DefaultPlanOptions())
	if len(plan.Victims) != 1 {
		t.Fatalf("plan victims = %+v, want one", plan.Victims)
	}

	archive := filepath.Join(t.TempDir(), "collapse-archive.jsonl")
	res, err := Apply(eng, plan, archive)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.VictimsDeleted != 1 || res.VictimsSkipped != 0 {
		t.Fatalf("result = %+v, want 1 deleted / 0 skipped", res)
	}
	if res.SegmentsRepointed != 1 || res.ObservationsDeleted != 1 {
		t.Fatalf("result = %+v, want 1 segment repointed / 1 observation deleted", res)
	}

	// Archive content.
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	var rec ArchiveRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &rec); err != nil {
		t.Fatalf("parse archive line: %v", err)
	}
	if rec.VictimID != victim || rec.SuccessorID != succ {
		t.Fatalf("archive record = %+v, want victim %s successor %s", rec, victim, succ)
	}
	if rec.Properties["content_full"] != "victim full content" {
		t.Fatalf("archive lost content_full: %v", rec.Properties)
	}
	if rec.Properties["resolution"] != "superseded" {
		t.Fatalf("archive lost resolution: %v", rec.Properties)
	}

	// Graph state.
	eng.RLock()
	defer eng.RUnlock()
	if _, ok := eng.Graph().GetNode(victim); ok {
		t.Fatal("victim survived apply")
	}
	if _, ok := eng.Graph().GetNode(obs.ID); ok {
		t.Fatal("observation child survived the cascade")
	}
	if _, ok := eng.Graph().GetNode(succ); !ok {
		t.Fatal("successor was deleted")
	}
	if ids := eng.PropIdx().Lookup("resolution", graph.StringProperty("superseded")); len(ids) != 0 {
		t.Fatalf("property index still lists the victim: %v", ids)
	}
	segNode, ok := eng.Graph().GetNode(seg.ID)
	if !ok {
		t.Fatal("segment was deleted; segments are append-only")
	}
	if captured, _ := segNode.Properties.GetString("captured_as"); captured != succ {
		t.Fatalf("segment captured_as = %s, want successor %s", captured, succ)
	}
	var repointed bool
	for _, e := range eng.Graph().EdgesFrom(seg.ID) {
		if e.Type == "extracted_as" && e.TargetID == succ {
			repointed = true
		}
		if e.Type == "extracted_as" && e.TargetID == victim {
			t.Fatal("stale extracted_as edge to the deleted victim survived")
		}
	}
	if !repointed {
		t.Fatal("segment provenance not re-pointed to the successor")
	}
}

// TestApplyRefusesExistingArchive pins the forever-insurance rule:
// an existing file at the archive path aborts before any mutation.
func TestApplyRefusesExistingArchive(t *testing.T) {
	eng := testutil.NewEngine(t)
	succ := addRecord(t, eng, "successor", false)
	victim := addRecord(t, eng, "victim", true)
	addSupersedes(t, eng, succ, victim, 0.95)
	save(t, eng)

	archive := filepath.Join(t.TempDir(), "existing.jsonl")
	if err := os.WriteFile(archive, []byte("occupied\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := BuildPlan(eng, DefaultPlanOptions())
	if _, err := Apply(eng, plan, archive); err == nil {
		t.Fatal("Apply overwrote an existing archive")
	}
	eng.RLock()
	defer eng.RUnlock()
	if _, ok := eng.Graph().GetNode(victim); !ok {
		t.Fatal("victim deleted despite the archive refusal")
	}
}

// TestApplySkipsChangedVictim pins the stale-evidence guard: a
// victim whose resolution changed between plan and apply is skipped,
// never deleted.
func TestApplySkipsChangedVictim(t *testing.T) {
	eng := testutil.NewEngine(t)
	succ := addRecord(t, eng, "successor", false)
	victim := addRecord(t, eng, "victim", true)
	addSupersedes(t, eng, succ, victim, 0.95)
	save(t, eng)

	plan := BuildPlan(eng, DefaultPlanOptions())

	eng.Lock()
	eng.SetProp(victim, "resolution", graph.StringProperty("completed"))
	eng.Unlock()

	archive := filepath.Join(t.TempDir(), "skip.jsonl")
	res, err := Apply(eng, plan, archive)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.VictimsDeleted != 0 || res.VictimsSkipped != 1 {
		t.Fatalf("result = %+v, want 0 deleted / 1 skipped", res)
	}
	eng.RLock()
	defer eng.RUnlock()
	if _, ok := eng.Graph().GetNode(victim); !ok {
		t.Fatal("re-resolved victim was deleted on stale plan evidence")
	}
}

// TestBuildPlanCycleDefers pins the cycle guard: two records that
// supersede each other (both carrying selection props) have no live
// chain head, so both defer instead of deleting each other.
func TestBuildPlanCycleDefers(t *testing.T) {
	eng := testutil.NewEngine(t)

	x := addRecord(t, eng, "cycle member one", true)
	y := addRecord(t, eng, "cycle member two", true)
	addSupersedes(t, eng, x, y, 0.95)
	addSupersedes(t, eng, y, x, 0.95)
	save(t, eng)

	plan := BuildPlan(eng, DefaultPlanOptions())
	if len(plan.Victims) != 0 {
		t.Fatalf("victims = %+v, want none for a supersedes cycle", plan.Victims)
	}
	if len(plan.Deferred) != 2 {
		t.Fatalf("deferred = %+v, want both cycle members", plan.Deferred)
	}
	for _, d := range plan.Deferred {
		if !strings.Contains(d.Reason, "no live successor") {
			t.Errorf("deferral reason %q does not name the missing live head", d.Reason)
		}
	}
}
