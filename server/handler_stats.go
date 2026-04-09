package server

import (
	"net/http"

	"github.com/gramaton-ai/gramaton/graph"
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
	result, _ := s.serviceStats()
	s.writeJSON(w, http.StatusOK, result)
}

func isChunkNode(g graph.NodeReader, id string) bool { return g.IsStructuralChild(id) }
