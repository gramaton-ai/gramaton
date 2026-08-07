// Package migrate holds one-off store migrations too heavy for the
// backfill sweeps in cli/backfill.go. Its first resident is the
// supersession collapse: fold pre-mutable-records supersession chains
// out of the live graph, leaving one live record per fact with the
// predecessors archived and hard-deleted.
package migrate

import (
	"fmt"
	"sort"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/search"
)

// PlanOptions tunes plan construction.
type PlanOptions struct {
	// MinWeight is the per-edge floor for auto-collapse. Supersedes
	// edges written by the retired auto-supersession path carry the
	// pair's cosine similarity (>= 0.92 by construction); edges from
	// the retired LLM contradiction verdict carry the model's
	// confidence instead and can sit lower. A component containing
	// any edge below the floor is deferred whole to manual review --
	// per-edge skipping could strand chain tails.
	MinWeight float64
	// ForkCensusThreshold is the similarity floor for the report-only
	// census of live near-duplicate pairs (the co-current class the
	// edge-keyed selection structurally cannot see).
	ForkCensusThreshold float64
	// ForkCensusMaxPairs bounds the census report.
	ForkCensusMaxPairs int
}

// DefaultPlanOptions returns the documented defaults.
func DefaultPlanOptions() PlanOptions {
	return PlanOptions{
		MinWeight:           0.92,
		ForkCensusThreshold: 0.92,
		ForkCensusMaxPairs:  50,
	}
}

// Victim is one record the plan will archive and hard-delete.
type Victim struct {
	ID           string    `json:"id"`
	SuccessorID  string    `json:"successor_id"`
	EdgeWeight   float64   `json:"edge_weight"`
	ContentShort string    `json:"content_short,omitempty"`
	Resolution   string    `json:"resolution"`
	ValidUntil   time.Time `json:"valid_until"`
	// Collections lists the victim's member_of collection IDs; the
	// plan only includes victims whose successor shares every one.
	Collections []string `json:"collections,omitempty"`
	// SegmentIDs are session segments whose extracted_as/captured_as
	// provenance points at the victim and re-points to SuccessorID
	// before deletion.
	SegmentIDs []string `json:"segment_ids,omitempty"`
	// ObservationIDs are derived observation children cascaded with
	// the victim.
	ObservationIDs []string `json:"observation_ids,omitempty"`
	// SuccessorContentShort gives the plan reader the surviving side.
	SuccessorContentShort string `json:"successor_content_short,omitempty"`
}

// Deferred is a victim or component the plan routes to manual review
// instead of collapsing.
type Deferred struct {
	VictimID string `json:"victim_id"`
	Reason   string `json:"reason"`
}

// Anomaly is a store artifact the plan reports but never touches.
type Anomaly struct {
	Kind   string `json:"kind"` // lineage_edge_on_current | superseded_props_no_edge
	EdgeID string `json:"edge_id,omitempty"`
	NodeID string `json:"node_id"`
	Detail string `json:"detail"`
}

// Plan is the full dry-run output. Apply consumes only Victims;
// everything else is reporting.
type Plan struct {
	Victims   []Victim               `json:"victims"`
	Deferred  []Deferred             `json:"deferred,omitempty"`
	Anomalies []Anomaly              `json:"anomalies,omitempty"`
	ForkPairs []search.DuplicatePair `json:"fork_pairs,omitempty"`
	// TotalSupersedesEdges counts every supersedes edge examined,
	// selected or not, so the report never implies full coverage it
	// did not have.
	TotalSupersedesEdges int `json:"total_supersedes_edges"`
}

// BuildPlan constructs the collapse plan. Read-only: it takes the
// engine read lock and mutates nothing. The command never certifies
// the store clean -- deferred items and anomalies are what remains.
func BuildPlan(eng *core.Engine, opts PlanOptions) *Plan {
	eng.RLock()
	defer eng.RUnlock()

	g := eng.Graph()
	plan := &Plan{}

	// Selection is a conjunction (never valid_until alone): an
	// inbound supersedes edge AND resolution=superseded AND a
	// valid_until stamp. An edge whose target fails the prop check is
	// a lineage edge on a current record -- the manual vocabulary
	// D-A keeps -- and is reported, never deleted.
	var selected []selEdge
	victimSet := map[string]bool{}
	for _, e := range g.EdgesByType("supersedes") {
		plan.TotalSupersedesEdges++
		target, ok := g.GetNode(e.TargetID)
		if !ok {
			continue
		}
		res, _ := target.Properties.GetString("resolution")
		_, hasValidUntil := target.Properties.GetTimestamp("valid_until")
		if res != "superseded" || !hasValidUntil {
			plan.Anomalies = append(plan.Anomalies, Anomaly{
				Kind:   "lineage_edge_on_current",
				EdgeID: e.ID,
				NodeID: e.TargetID,
				Detail: fmt.Sprintf("supersedes edge %s -> %s targets a record without superseded resolution props; kept as manual lineage", e.SourceID, e.TargetID),
			})
			continue
		}
		selected = append(selected, selEdge{edge: e, victim: e.TargetID})
		victimSet[e.TargetID] = true
	}

	// Census (b): superseded-props records with NO inbound supersedes
	// edge (swallowed AddEdge failures in old recoveries). Report
	// only; the conjunction cannot select them.
	it := g.NodeIterator()
	for it.Next() {
		n := it.Node()
		res, _ := n.Properties.GetString("resolution")
		if res != "superseded" {
			continue
		}
		if _, ok := n.Properties.GetTimestamp("valid_until"); !ok {
			continue
		}
		hasInbound := false
		for _, e := range g.EdgesTo(n.ID) {
			if e.Type == "supersedes" {
				hasInbound = true
				break
			}
		}
		if !hasInbound {
			plan.Anomalies = append(plan.Anomalies, Anomaly{
				Kind:   "superseded_props_no_edge",
				NodeID: n.ID,
				Detail: "resolution=superseded with valid_until but no inbound supersedes edge; props-keyed follow-up candidate",
			})
		}
	}
	it.Close()

	// Chains plan as edge-connected components over the selected
	// edges, so a mixed-weight or cyclic chain defers WHOLE -- a
	// partial collapse must not destroy a tail's selection marker.
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == "" || parent[x] == x {
			parent[x] = x
			return x
		}
		root := find(parent[x])
		parent[x] = root
		return root
	}
	union := func(a, b string) { parent[find(a)] = find(b) }
	for _, se := range selected {
		union(se.edge.SourceID, se.edge.TargetID)
	}
	componentEdges := map[string][]selEdge{}
	for _, se := range selected {
		root := find(se.victim)
		componentEdges[root] = append(componentEdges[root], se)
	}

	deferredVictims := map[string]string{} // victim -> reason
	for _, edges := range componentEdges {
		var reason string
		for _, se := range edges {
			if se.edge.Weight < opts.MinWeight {
				reason = fmt.Sprintf("component contains edge %s at weight %.3f below the %.2f floor", se.edge.ID, se.edge.Weight, opts.MinWeight)
				break
			}
		}
		if reason == "" && componentHasCycle(edges) {
			reason = "supersedes cycle in component"
		}
		if reason != "" {
			for _, se := range edges {
				if _, dup := deferredVictims[se.victim]; !dup {
					deferredVictims[se.victim] = reason
				}
			}
		}
	}

	// Collection co-membership (per victim): the successor must share
	// every collection the victim belongs to, or the pair defers --
	// deleting the victim would silently shrink a collection the
	// successor never joined.
	bestInbound := map[string]*graph.Edge{}
	for _, se := range selected {
		cur := bestInbound[se.victim]
		if cur == nil || se.edge.Weight > cur.Weight {
			bestInbound[se.victim] = se.edge
		}
	}
	memberOf := func(id string) []string {
		var out []string
		for _, e := range g.EdgesFrom(id) {
			if e.Type == "member_of" {
				out = append(out, e.TargetID)
			}
		}
		sort.Strings(out)
		return out
	}

	// Successor resolution walks max-weight inbound edges up the
	// chain until it reaches a node that will remain live: a
	// non-victim, or a victim already deferred (it survives this
	// run, so provenance may point at it).
	liveSuccessor := func(victimID string) (string, bool) {
		cur := victimID
		for range len(selected) + 1 {
			e := bestInbound[cur]
			if e == nil {
				return "", false
			}
			next := e.SourceID
			if !victimSet[next] {
				return next, true
			}
			if _, def := deferredVictims[next]; def {
				return next, true
			}
			cur = next
		}
		return "", false
	}

	for victimID := range victimSet {
		if reason, def := deferredVictims[victimID]; def {
			plan.Deferred = append(plan.Deferred, Deferred{VictimID: victimID, Reason: reason})
			continue
		}
		n, ok := g.GetNode(victimID)
		if !ok {
			continue
		}
		succID, ok := liveSuccessor(victimID)
		if !ok {
			plan.Deferred = append(plan.Deferred, Deferred{VictimID: victimID, Reason: "no live successor reachable (broken chain head)"})
			continue
		}

		vCols := memberOf(victimID)
		if len(vCols) > 0 {
			sCols := map[string]bool{}
			for _, c := range memberOf(succID) {
				sCols[c] = true
			}
			shared := true
			for _, c := range vCols {
				if !sCols[c] {
					shared = false
					break
				}
			}
			if !shared {
				plan.Deferred = append(plan.Deferred, Deferred{VictimID: victimID, Reason: fmt.Sprintf("collections not shared by successor %s", succID)})
				continue
			}
		}

		v := Victim{
			ID:          victimID,
			SuccessorID: succID,
			EdgeWeight:  bestInbound[victimID].Weight,
			Collections: vCols,
		}
		v.ContentShort, _ = n.Properties.GetString("content_short")
		v.Resolution, _ = n.Properties.GetString("resolution")
		v.ValidUntil, _ = n.Properties.GetTimestamp("valid_until")
		if succ, ok := g.GetNode(succID); ok {
			v.SuccessorContentShort, _ = succ.Properties.GetString("content_short")
		}
		for _, e := range g.EdgesTo(victimID) {
			switch e.Type {
			case "extracted_as":
				v.SegmentIDs = append(v.SegmentIDs, e.SourceID)
			case "observation_of":
				if child, ok := g.GetNode(e.SourceID); ok {
					if nt, _ := child.Properties.GetString("node_type"); nt == "observation" {
						v.ObservationIDs = append(v.ObservationIDs, e.SourceID)
					}
				}
			}
		}
		sort.Strings(v.SegmentIDs)
		sort.Strings(v.ObservationIDs)
		plan.Victims = append(plan.Victims, v)
	}

	sort.Slice(plan.Victims, func(i, j int) bool { return plan.Victims[i].ID < plan.Victims[j].ID })
	sort.Slice(plan.Deferred, func(i, j int) bool { return plan.Deferred[i].VictimID < plan.Deferred[j].VictimID })

	// Fork census (a): live near-duplicate pairs at the threshold.
	// The edge-keyed selection structurally cannot see co-current
	// duplicates; they are manual merge-via-update material. Both
	// sides must be live -- pairs involving historical records are
	// either already in the plan or already resolved.
	for _, p := range search.FindDuplicates(g, eng.VecIdx(), opts.ForkCensusThreshold, opts.ForkCensusMaxPairs) {
		if isLive(g, p.IDA) && isLive(g, p.IDB) {
			plan.ForkPairs = append(plan.ForkPairs, p)
		}
	}

	return plan
}

// selEdge is one selection-passing supersedes edge and its victim
// (the edge's target).
type selEdge struct {
	edge   *graph.Edge
	victim string
}

// componentHasCycle detects a cycle in one component's selected
// supersedes edges (successor -> victim direction).
func componentHasCycle(edges []selEdge) bool {
	adj := map[string][]string{}
	nodes := map[string]bool{}
	for _, se := range edges {
		adj[se.edge.SourceID] = append(adj[se.edge.SourceID], se.edge.TargetID)
		nodes[se.edge.SourceID] = true
		nodes[se.edge.TargetID] = true
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var visit func(string) bool
	visit = func(u string) bool {
		color[u] = gray
		for _, v := range adj[u] {
			switch color[v] {
			case gray:
				return true
			case white:
				if visit(v) {
					return true
				}
			}
		}
		color[u] = black
		return false
	}
	for u := range nodes {
		if color[u] == white && visit(u) {
			return true
		}
	}
	return false
}

// isLive reports whether a record carries no valid_until (current
// knowledge).
func isLive(g *graph.Graph, id string) bool {
	n, ok := g.GetNode(id)
	if !ok {
		return false
	}
	_, has := n.Properties.GetTimestamp("valid_until")
	return !has
}
