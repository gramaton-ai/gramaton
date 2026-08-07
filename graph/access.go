package graph

import "time"

// RecordAccess updates a node's access bookkeeping. Call when a node
// is returned to a consumer (search result, inspect, etc.).
// Increments access_count and sets last_accessed to now -- display,
// sorts, filters, and GC eligibility consume these; they play no
// scoring role. The former neighbor activation spread is gone with
// the activation term itself: it dirtied the accessed node's whole
// neighborhood on every read, the mechanism behind read-driven
// commit churn.
//
// The lookup goes through GetNode so the call works correctly in
// lazy mode. Mutation of n.Properties assumes the engine write lock
// is held by the caller.
func (g *Graph) RecordAccess(nodeID string, now time.Time) {
	n, ok := g.GetNode(nodeID)
	if !ok {
		return
	}
	var count int64
	if v, ok := n.Properties.GetInt64("access_count"); ok {
		count = v
	}
	n.Properties["access_count"] = Int64Property(count + 1)
	n.Properties["last_accessed"] = TimestampProperty(now)
	g.markNodeDirty(nodeID)
}
