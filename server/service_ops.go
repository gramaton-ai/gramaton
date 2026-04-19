package server

import (
	"github.com/gramaton-ai/gramaton/graph"
)

// servicePending lists records awaiting classification.
func (s *Server) servicePending(limit int) (map[string]any, *serviceError) {
	if limit <= 0 {
		limit = 50
	}

	s.engine.RLock()
	defer s.engine.RUnlock()

	captured := s.engine.PropIdx().Lookup("processing_status",
		graph.StringProperty("captured"))

	var records []map[string]any
	for _, id := range captured {
		if len(records) >= limit {
			break
		}
		entry := map[string]any{"id": id}
		if n, ok := s.engine.Graph().GetNode(id); ok {
			if v, ok := n.Properties.GetString("content_short"); ok {
				entry["summary_short"] = v
			}
			if v, ok := n.Properties.GetTimestamp("created_at"); ok {
				entry["created_at"] = v.Format("2006-01-02T15:04:05Z")
			}
		}
		records = append(records, entry)
	}

	if records == nil {
		records = []map[string]any{}
	}

	resp := map[string]any{"records": records, "total": len(captured)}
	if len(captured) > limit {
		resp["truncated"] = true
	}
	return resp, nil
}
