package graph

import "time"

// ActivationConfig controls spreading activation behavior.
type ActivationConfig struct {
	BaseAmount        float64
	AttenuationFactor float64
}

// RecordAccess updates a node's access metadata and spreads activation
// to its one-hop neighbors. Call this when a node is returned to a
// consumer (search result, inspect, etc.).
//
// Direct access: increments access_count, sets last_accessed to now.
// Neighbor activation: for each edge from the accessed node, adds
// base_amount * edge_weight * attenuation_factor to the neighbor's
// activation_boost.
func (g *Graph) RecordAccess(nodeID string, now time.Time, cfg ActivationConfig) {
	n, ok := g.nodes[nodeID]
	if !ok {
		return
	}

	// Direct access updates.
	var count int64
	if v, ok := n.Properties.GetInt64("access_count"); ok {
		count = v
	}
	n.Properties["access_count"] = Int64Property(count + 1)
	n.Properties["last_accessed"] = TimestampProperty(now)
	g.markNodeDirty(nodeID)

	// Spread activation to neighbors via outbound edges.
	if outs, ok := g.outEdges[nodeID]; ok {
		for eid := range outs {
			e := g.edges[eid]
			boostNeighbor(g, e.TargetID, cfg.BaseAmount*e.Weight*cfg.AttenuationFactor)
		}
	}

	// Spread activation to neighbors via inbound edges.
	if ins, ok := g.inEdges[nodeID]; ok {
		for eid := range ins {
			e := g.edges[eid]
			boostNeighbor(g, e.SourceID, cfg.BaseAmount*e.Weight*cfg.AttenuationFactor)
		}
	}
}

func boostNeighbor(g *Graph, neighborID string, amount float64) {
	n, ok := g.nodes[neighborID]
	if !ok {
		return
	}
	var current float64
	if v, ok := n.Properties.GetFloat64("activation_boost"); ok {
		current = v
	}
	n.Properties["activation_boost"] = Float64Property(current + amount)
	g.markNodeDirty(neighborID)
}
