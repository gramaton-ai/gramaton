package migrate

import (
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

// TestBuildPlanMixedWeightDefersWholeComponent pins F5: one
// below-floor edge defers the entire chain, not just its own pair --
// partial collapse must not strand the tail's selection marker.
func TestBuildPlanMixedWeightDefersWholeComponent(t *testing.T) {
	eng := testutil.NewEngine(t)

	a := addRecord(t, eng, "head", false)
	b := addRecord(t, eng, "victim strong edge", true)
	c := addRecord(t, eng, "victim weak edge", true)
	addSupersedes(t, eng, a, b, 0.95)
	addSupersedes(t, eng, b, c, 0.60) // LLM-confidence-path weight
	save(t, eng)

	plan := BuildPlan(eng, DefaultPlanOptions())
	if len(plan.Victims) != 0 {
		t.Fatalf("victims = %+v, want none (mixed-weight component defers whole)", plan.Victims)
	}
	if len(plan.Deferred) != 2 {
		t.Fatalf("deferred = %+v, want both chain victims", plan.Deferred)
	}
	for _, d := range plan.Deferred {
		if !strings.Contains(d.Reason, "below the 0.92 floor") {
			t.Errorf("deferral reason %q does not name the weight floor", d.Reason)
		}
	}
}

// TestBuildPlanCollectionMismatchDefers pins F19: a victim belonging
// to a collection the successor does not share defers to manual
// review; a co-member victim collapses.
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
