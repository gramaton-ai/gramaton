package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/brandonlattin/gramaton/llm"
	"github.com/brandonlattin/gramaton/search"
)

// RelevanceGrade indicates how relevant a record is to a query.
type RelevanceGrade int

const (
	NotRelevant    RelevanceGrade = 0
	Marginal       RelevanceGrade = 1
	Relevant       RelevanceGrade = 2
	HighlyRelevant RelevanceGrade = 3
)

// RetrievalQuery defines a test query with expected relevance judgments.
type RetrievalQuery struct {
	Name      string
	Text      string                        // natural language query
	QueryVec  []float32                     // pre-embedded vector (768-dim)
	Judgments map[string]RelevanceGrade     // PopulatedStore field name -> grade
	Filters   search.Query                  // optional metadata filters
}

// CaptureGroundTruth defines the expected classification for a content
// sample. AltXxx fields list acceptable alternative values for ambiguous
// cases.
type CaptureGroundTruth struct {
	Name               string   `json:"name"`
	Content            string   `json:"content"`
	Temporality        string   `json:"temporality"`
	Confidence         float64  `json:"confidence"`
	KnowledgeType      string   `json:"knowledge_type"`
	EpistemicStatus    string   `json:"epistemic_status"`
	Keywords           []string `json:"keywords"`
	AltTemporality     []string `json:"alt_temporality,omitempty"`
	AltKnowledgeType   []string `json:"alt_knowledge_type,omitempty"`
	AltEpistemicStatus []string `json:"alt_epistemic_status,omitempty"`
}

// Classification is the output of a Classifier.
type Classification struct {
	Temporality     string   `json:"temporality"`
	Confidence      float64  `json:"confidence"`
	KnowledgeType   string   `json:"knowledge_type"`
	EpistemicStatus string   `json:"epistemic_status"`
	Keywords        []string `json:"keywords"`
	SummaryShort    string   `json:"summary_short"`
}

// Classifier produces metadata classifications from content.
type Classifier interface {
	Classify(ctx context.Context, content string) (*Classification, error)
}

// MockClassifier returns reasonable defaults for all content. It tests
// the evaluation framework itself, not classification quality.
type MockClassifier struct{}

func (m *MockClassifier) Classify(_ context.Context, _ string) (*Classification, error) {
	return &Classification{
		Temporality:     "durable",
		Confidence:      0.7,
		KnowledgeType:   "semantic",
		EpistemicStatus: "well_established",
		Keywords:        []string{"general"},
		SummaryShort:    "mock classification",
	}, nil
}

// LLMClassifier uses an LLM provider to classify content. Uses the
// same prompt structure as curation/prompts.go:classifyPrompt.
type LLMClassifier struct {
	LLM llm.Provider
}

const classifyPrompt = `Classify this knowledge record. Respond with JSON only, no other text.

Content:
%s

Respond with this exact JSON structure:
{
  "temporality": "immutable|durable|temporal|ephemeral",
  "confidence": 0.0-1.0,
  "knowledge_type": "episodic|semantic|procedural|conceptual|reference",
  "epistemic_status": "well_established|probable|speculative|contested|refuted",
  "keywords": ["keyword1", "keyword2"],
  "summary_short": "max 200 character summary"
}

Classification guide:
- temporality: How long will this remain valid? immutable=forever (definitions), durable=until contradicted, temporal=time-bound, ephemeral=hours
- confidence: How likely is this correct? 0.9+=authoritative, 0.7-0.9=well-supported, 0.4-0.7=uncertain, <0.4=speculative
- knowledge_type: episodic=event, semantic=fact, procedural=how-to, conceptual=principle, reference=lookup data
- epistemic_status: well_established=broadly accepted, probable=likely, speculative=uncertain, contested=disagreement, refuted=false
- keywords: 3-8 specific, searchable terms. Prefer concrete nouns over abstract words.
- summary_short: Capture the essence in under 200 characters. Start with the key fact or decision.`

func (c *LLMClassifier) Classify(ctx context.Context, content string) (*Classification, error) {
	prompt := fmt.Sprintf(classifyPrompt, content)
	resp, err := c.LLM.Complete(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("llm complete: %w", err)
	}

	// Strip markdown code fences if present.
	text := strings.TrimSpace(resp)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var cls Classification
	if err := json.Unmarshal([]byte(text), &cls); err != nil {
		return nil, fmt.Errorf("parse classification: %w (raw: %s)", err, text)
	}
	return &cls, nil
}
