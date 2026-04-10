package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/graph"
	"github.com/gramaton-ai/gramaton/index"
)

type observeRequest struct {
	Messages []observeMessage `json:"messages,omitempty"`
	Facts    []string         `json:"facts,omitempty"`
}

type observeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// handleObserve accepts conversation messages or pre-extracted facts,
// returns immediately, and processes asynchronously. This is the
// "observe" half of the dual-mode capture system.
func (s *Server) handleObserve(w http.ResponseWriter, r *http.Request) {
	var req observeRequest
	if err := parseJSON(r, &req, maxJSONBodySize); err != nil {
		s.writeError(w, http.StatusBadRequest, "input_error", err.Error(), true)
		return
	}

	result, svcErr := s.serviceObserve(req)
	if svcErr != nil {
		s.writeServiceError(w, svcErr)
		return
	}

	s.writeJSON(w, http.StatusAccepted, result)
}

// processObservation runs extraction and quality gates in the background.
func (s *Server) processObservation(req observeRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg := s.engine.Config()
	var facts []string

	if len(req.Facts) > 0 {
		// Facts mode: skip extraction, use provided facts directly.
		facts = req.Facts
	} else {
		// Messages mode: extract facts via LLM.
		extracted, err := s.extractFacts(ctx, req.Messages)
		if err != nil {
			s.log.Warn("observe extraction failed",
				"component", "observe", "err", err)
			return
		}
		facts = extracted
	}

	// Cap facts per call.
	maxFacts := cfg.Observe.MaxFactsPerCall
	if maxFacts <= 0 {
		maxFacts = 20
	}
	if len(facts) > maxFacts {
		facts = facts[:maxFacts]
	}

	// Run quality gates and store survivors.
	stored := s.applyQualityGates(ctx, facts, cfg)

	if stored > 0 {
		s.log.Info("observe pipeline complete",
			"component", "observe",
			"extracted", len(facts),
			"stored", stored)
	}
}

// extractFacts uses the server LLM to extract candidate facts from
// conversation messages.
func (s *Server) extractFacts(ctx context.Context, messages []observeMessage) ([]string, error) {
	llmProv := s.engine.LLM()
	if llmProv == nil {
		return nil, fmt.Errorf("no LLM provider configured")
	}

	// Build conversation text for the prompt.
	var sb strings.Builder
	for _, m := range messages {
		sb.WriteString(m.Role)
		sb.WriteString(": ")
		sb.WriteString(m.Content)
		sb.WriteString("\n\n")
	}

	prompt := fmt.Sprintf(observeExtractionPrompt, sb.String())
	resp, err := llmProv.Complete(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM extraction: %w", err)
	}

	return parseExtractedFacts(resp)
}

// parseExtractedFacts parses the LLM's JSON response into a list of facts.
func parseExtractedFacts(resp string) ([]string, error) {
	resp = strings.TrimSpace(resp)

	// Strip markdown code fences if present.
	if strings.HasPrefix(resp, "```") {
		lines := strings.Split(resp, "\n")
		var jsonLines []string
		inBlock := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock {
				jsonLines = append(jsonLines, line)
			}
		}
		resp = strings.Join(jsonLines, "\n")
	}

	// Find JSON object.
	start := strings.Index(resp, "{")
	end := strings.LastIndex(resp, "}")
	if start >= 0 && end > start {
		resp = resp[start : end+1]
	}

	var result struct {
		Facts []string `json:"facts"`
	}
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("parse extraction JSON: %w", err)
	}

	return result.Facts, nil
}

// applyQualityGates filters extracted facts and stores survivors.
// Returns the number of facts stored.
func (s *Server) applyQualityGates(ctx context.Context, facts []string, cfg config.Config) int {
	stored := 0
	var gateSubstance, gateDedup, gateRecency, gateRetrieval int

	// Gather retrieval tracker IDs and their embeddings for Gate 3.
	var retrievedEmbeddings map[string][]float32
	if cfg.Observe.RetrievalTracking {
		retrievedIDs := s.retrieval.RetrievedIDs()
		if len(retrievedIDs) > 0 {
			retrievedEmbeddings = s.gatherRetrievedEmbeddings(retrievedIDs)
		}
	}

	minLength := cfg.Observe.SubstanceMinLength
	if minLength <= 0 {
		minLength = 20
	}

	for _, fact := range facts {
		fact = strings.TrimSpace(fact)

		// Gate 4: Substance filter.
		if len(fact) < minLength {
			gateSubstance++
			continue
		}

		// Embed the fact for similarity checks.
		if s.engine.Embedder() == nil {
			// No embedder: skip similarity gates, store directly.
			s.storeDeferredCapture(fact, cfg)
			stored++
			continue
		}

		vecs, err := s.engine.Embedder().Embed(ctx, []string{fact})
		if err != nil || len(vecs) == 0 {
			// Embedding failed: store without similarity checks.
			s.storeDeferredCapture(fact, cfg)
			stored++
			continue
		}
		factVec := vecs[0]

		// Gate 1: Store-wide dedup (0.92 threshold).
		similar := s.searchSimilarFacts(factVec, 5)

		skipFact := false
		skipGate := 0 // which gate caused the skip
		feedbackHours := cfg.Observe.FeedbackLoopHours
		if feedbackHours <= 0 {
			feedbackHours = 4
		}
		recencyCutoff := time.Now().UTC().Add(-time.Duration(feedbackHours) * time.Hour)

		for _, sr := range similar {
			sim := float64(sr.Similarity)

			// Gate 1: exact/near dedup.
			if sim >= 0.92 {
				skipFact = true
				skipGate = 1
				break
			}

			// Gate 2: recency check.
			if sim >= cfg.Observe.FeedbackLoopSimilarity {
				if s.nodeAccessedAfter(sr.NodeID, recencyCutoff) {
					skipFact = true
					skipGate = 2
					break
				}
			}
		}

		if skipFact {
			if skipGate == 1 {
				gateDedup++
			} else {
				gateRecency++
			}
			continue
		}

		// Gate 3: Retrieval tracker check (0.7 threshold).
		if cfg.Observe.RetrievalTracking && len(retrievedEmbeddings) > 0 {
			for _, retVec := range retrievedEmbeddings {
				sim := index.CosineSimilarity(factVec, retVec)
				if float64(sim) >= cfg.Observe.RetrievalSimilarity {
					skipFact = true
					break
				}
			}
			if skipFact {
				gateRetrieval++
				continue
			}
		}

		// All gates passed. Store with embedding.
		s.storeDeferredCaptureWithEmbedding(fact, factVec, cfg)
		stored++
	}

	rejected := gateSubstance + gateDedup + gateRecency + gateRetrieval
	if rejected > 0 {
		s.log.Debug("observe quality gates",
			"component", "observe",
			"input", len(facts),
			"stored", stored,
			"rejected_substance", gateSubstance,
			"rejected_dedup", gateDedup,
			"rejected_recency", gateRecency,
			"rejected_retrieval", gateRetrieval)
	}

	return stored
}

// gatherRetrievedEmbeddings reads embeddings for the given node IDs
// under a single read lock with defer.
func (s *Server) gatherRetrievedEmbeddings(ids []string) map[string][]float32 {
	s.engine.RLock()
	defer s.engine.RUnlock()
	out := make(map[string][]float32, len(ids))
	for _, id := range ids {
		if n, ok := s.engine.Graph().GetNode(id); ok {
			if emb, ok := n.Properties.GetVector("embedding_full"); ok {
				out[id] = emb
			}
		}
	}
	return out
}

// searchSimilarFacts searches the vector index under a read lock with defer.
func (s *Server) searchSimilarFacts(vec []float32, top int) []index.SearchResult {
	s.engine.RLock()
	defer s.engine.RUnlock()
	return s.engine.VecIdx().Search(vec, top, nil)
}

// nodeAccessedAfter checks if a node's last_accessed is after cutoff,
// under a read lock with defer.
func (s *Server) nodeAccessedAfter(nodeID string, cutoff time.Time) bool {
	s.engine.RLock()
	defer s.engine.RUnlock()
	n, ok := s.engine.Graph().GetNode(nodeID)
	if !ok {
		return false
	}
	la, ok := n.Properties.GetTimestamp("last_accessed")
	if !ok {
		return false
	}
	return la.After(cutoff)
}

// storeDeferredCapture stores a fact as a deferred capture with
// conservative defaults.
func (s *Server) storeDeferredCapture(fact string, cfg config.Config) {
	s.engine.Lock()
	defer s.engine.Unlock()

	now := time.Now().UTC()
	props := graph.Properties{
		"content_full":      graph.StringProperty(fact),
		"processing_status": graph.StringProperty("captured"),
		"temporality":       graph.StringProperty(cfg.Observe.DefaultTemporality),
		"confidence":        graph.Float64Property(cfg.Observe.DefaultConfidence),
		"importance":        graph.Float64Property(0),
		"source_credibility": graph.Float64Property(0.5),
		"testimony_hops":    graph.Int64Property(1),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
		"source_ref":        graph.StringProperty(fmt.Sprintf("observe:%s", now.Format(time.RFC3339))),
	}
	n := s.engine.Graph().AddNode(props)
	s.engine.IndexNode(n.ID, fact, nil)
	s.engine.Save("observe")
}

// storeDeferredCaptureWithEmbedding stores a fact with its pre-computed
// embedding vector.
func (s *Server) storeDeferredCaptureWithEmbedding(fact string, vec []float32, cfg config.Config) {
	s.engine.Lock()
	defer s.engine.Unlock()

	now := time.Now().UTC()
	modelID := ""
	if s.engine.Embedder() != nil {
		modelID = s.engine.Embedder().ModelID()
	}

	props := graph.Properties{
		"content_full":      graph.StringProperty(fact),
		"processing_status": graph.StringProperty("captured"),
		"temporality":       graph.StringProperty(cfg.Observe.DefaultTemporality),
		"confidence":        graph.Float64Property(cfg.Observe.DefaultConfidence),
		"importance":        graph.Float64Property(0),
		"source_credibility": graph.Float64Property(0.5),
		"testimony_hops":    graph.Int64Property(1),
		"created_at":        graph.TimestampProperty(now),
		"access_count":      graph.Int64Property(0),
		"source_ref":        graph.StringProperty(fmt.Sprintf("observe:%s", now.Format(time.RFC3339))),
		"embedding_full":    graph.VectorProperty(vec),
	}
	if modelID != "" {
		props["embedding_model"] = graph.StringProperty(modelID)
	}

	n := s.engine.Graph().AddNode(props)
	s.engine.IndexNode(n.ID, fact, vec)
	s.engine.Save("observe")
}

// observeExtractionPrompt is the prompt used to extract facts from
// conversation messages. Domain-neutral, includes negative examples.
const observeExtractionPrompt = `Extract knowledge worth remembering from this conversation. Respond with JSON only, no other text.

Conversation:
%s

Respond with this exact JSON structure:
{"facts": ["fact1", "fact2", ...]}

Extract:
- Decisions made ("we will use X", "decided to go with Y")
- Preferences stated ("I prefer X", "always do it this way")
- Facts learned ("X works by doing Y", "the API requires Z")
- Procedures established ("to deploy, first do X then Y")
- Constraints identified ("we can't do X because Y")
- Requirements specified ("the system must support X")

Do NOT extract:
- Greetings, thanks, or small talk
- Questions that weren't answered
- Debugging steps or troubleshooting attempts that didn't lead to conclusions
- The assistant's explanations or analysis (extract the CONCLUSION, not the reasoning)
- Information that was merely read from a file or search result (already stored elsewhere)
- Incomplete thoughts or work-in-progress

Each fact should be a self-contained statement that would be useful to recall in a future session. If there is nothing worth extracting, return {"facts": []}.`
