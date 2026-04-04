package cli

import (
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/index"
)

// CurationStatus is appended to CLI read responses so the agent knows
// whether to spawn a curation subagent.
type CurationStatus struct {
	PendingCount int  `json:"pending_count"`
	Overdue      bool `json:"overdue"`
}

// computeCurationStatus checks how many records need classification.
func computeCurationStatus(g *graph.Graph, propIdx *index.PropertyIndex) CurationStatus {
	captured := propIdx.Lookup("processing_status", graph.StringProperty("captured"))
	return CurationStatus{
		PendingCount: len(captured),
		Overdue:      len(captured) > 0,
	}
}
