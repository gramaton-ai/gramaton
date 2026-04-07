package server

import (
	"net/http"

	"github.com/brandonlattin/gramaton/graph"
)

type statsResponse struct {
	TotalRecords    int            `json:"total_records"`
	Temporality     map[string]int `json:"temporality"`
	KnowledgeType   map[string]int `json:"knowledge_type"`
	EpistemicStatus map[string]int `json:"epistemic_status"`
	Confidence      confidenceDist `json:"confidence"`
}

type confidenceDist struct {
	High     int `json:"high"`     // >= 0.9
	Medium   int `json:"medium"`   // 0.7-0.9
	Moderate int `json:"moderate"` // 0.4-0.7
	Low      int `json:"low"`      // < 0.4
	Unset    int `json:"unset"`
}

func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	s.engine.RLock()
	defer s.engine.RUnlock()

	g := s.engine.Graph()
	resp := statsResponse{
		Temporality:     make(map[string]int),
		KnowledgeType:   make(map[string]int),
		EpistemicStatus: make(map[string]int),
	}

	for _, id := range g.AllNodeIDs() {
		n, ok := g.GetNode(id)
		if !ok {
			continue
		}

		// Skip chunk nodes and deleted records.
		if isChunkNode(g, id) {
			continue
		}
		if ps, ok := n.Properties.GetString("processing_status"); ok && ps == "deleted" {
			continue
		}

		resp.TotalRecords++

		if v, ok := n.Properties.GetString("temporality"); ok {
			resp.Temporality[v]++
		}
		if v, ok := n.Properties.GetString("knowledge_type"); ok {
			resp.KnowledgeType[v]++
		}
		if v, ok := n.Properties.GetString("epistemic_status"); ok {
			resp.EpistemicStatus[v]++
		}
		if c, ok := n.Properties.GetFloat64("confidence"); ok {
			switch {
			case c >= 0.9:
				resp.Confidence.High++
			case c >= 0.7:
				resp.Confidence.Medium++
			case c >= 0.4:
				resp.Confidence.Moderate++
			default:
				resp.Confidence.Low++
			}
		} else {
			resp.Confidence.Unset++
		}
	}

	s.writeJSONLocked(w, http.StatusOK, resp)
}

func isChunkNode(g *graph.Graph, id string) bool { return g.IsStructuralChild(id) }
