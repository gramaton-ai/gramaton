package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/awscfg"
)

// modelAccessHint mirrors llm/bedrock's: AccessDeniedException
// covers both IAM-permission-missing and "model access not granted
// in console". Surface the docs link so users have a clear next
// step.
const modelAccessHint = "AccessDeniedException can mean (1) the IAM principal lacks bedrock:InvokeModel for this model, OR (2) the model has not been enabled in the Bedrock console for this account/region. To enable model access: https://docs.aws.amazon.com/bedrock/latest/userguide/model-access-modify.html"

// Client is an embedding provider that calls Amazon Bedrock's
// InvokeModel API. Supports Titan Embed and Cohere Embed model
// families (detected by model ID prefix).
type Client struct {
	client *bedrockruntime.Client
	model  string
	family modelFamily

	// accessDeniedWarned dedups the per-model AccessDeniedException
	// hint so a curation reembed loop doesn't repeat the warn line
	// for every batch on a non-enabled model.
	accessDeniedWarned sync.Map
}

// classifyBedrockError mirrors the LLM client's helper. Wraps
// AccessDeniedException with the model-access hint and
// ResourceNotFoundException with a model-id / region-availability
// hint. Logs the AccessDenied case once per model via the dedup
// map so a tight reembed loop on a non-enabled model doesn't flood
// the log.
func (c *Client) classifyBedrockError(err error, model string) error {
	if err == nil {
		return nil
	}
	var ade *types.AccessDeniedException
	if errors.As(err, &ade) {
		c.warnAccessDenied(model)
		return fmt.Errorf("%w (hint: %s)", err, modelAccessHint)
	}
	var rnf *types.ResourceNotFoundException
	if errors.As(err, &rnf) {
		return fmt.Errorf("%w (hint: model %q is not available in this region; check region setting and the model-id format)", err, model)
	}
	return err
}

// warnAccessDenied dedup-warns once per model.
func (c *Client) warnAccessDenied(model string) {
	if _, loaded := c.accessDeniedWarned.LoadOrStore(model, struct{}{}); loaded {
		return
	}
	slog.Warn("bedrock embed: AccessDeniedException -- check model access + IAM",
		"component", "embed",
		"model", model,
		"hint", modelAccessHint)
}

type modelFamily int

const (
	familyTitan  modelFamily = iota
	familyCohere
)

// New creates a Bedrock embedding client from the embedding config.
// Mirrors the LLM client's logging contract: one Info on
// construction with (region, profile-set, model, family); per-call
// refresh / retry diagnostics flow through awscfg's slog adapter.
func New(cfg config.EmbeddingConfig) (*Client, error) {
	family, err := detectFamily(cfg.Model)
	if err != nil {
		return nil, err
	}

	awsCfg, err := awscfg.Load(context.Background(), cfg.Region, cfg.AWSProfile,
		cfg.AWSAccessKeyIDEnv, cfg.AWSSecretAccessKeyEnv)
	if err != nil {
		return nil, fmt.Errorf("bedrock embed: load AWS config: %w", err)
	}

	familyName := "titan"
	if family == familyCohere {
		familyName = "cohere"
	}
	slog.Info("bedrock embed: client initialized",
		"component", "embed",
		"region", cfg.Region,
		"profile_set", cfg.AWSProfile != "",
		"model", cfg.Model,
		"family", familyName)

	return &Client{
		client: bedrockruntime.NewFromConfig(awsCfg),
		model:  cfg.Model,
		family: family,
	}, nil
}

// Embed generates embeddings for the given texts. Titan models are
// called one at a time (no native batching). Cohere models support
// batch input. Cohere calls mark input_type="search_document" so
// indexed content embeds correctly; use EmbedQuery for retrieval-
// time queries.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	switch c.family {
	case familyTitan:
		return c.embedTitan(ctx, texts)
	case familyCohere:
		return c.embedCohere(ctx, texts, "search_document")
	default:
		return nil, fmt.Errorf("bedrock embed: unsupported model family")
	}
}

// EmbedQuery generates a single embedding for a retrieval query.
// For Cohere, this sets input_type="search_query" -- Cohere produces
// different vectors for query vs document inputs, and using the
// document path for a query degrades cosine similarity measurably.
// For Titan (no query/document distinction in the API), delegates to
// Embed. Implements embed.QueryEmbedder.
func (c *Client) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	switch c.family {
	case familyCohere:
		vecs, err := c.embedCohere(ctx, []string{text}, "search_query")
		if err != nil {
			return nil, err
		}
		if len(vecs) == 0 {
			return nil, nil
		}
		return vecs[0], nil
	default:
		// Titan and any other family: no distinction at the API level.
		vecs, err := c.Embed(ctx, []string{text})
		if err != nil {
			return nil, err
		}
		if len(vecs) == 0 {
			return nil, nil
		}
		return vecs[0], nil
	}
}

// ModelID returns the Bedrock model identifier.
func (c *Client) ModelID() string {
	return c.model
}

// ContextWindow returns 0 (unknown). Bedrock does not expose model
// context limits via API. Callers should use config or default.
func (c *Client) ContextWindow() int {
	return 0
}

// --- Titan Embed V2 ---

type titanRequest struct {
	InputText string `json:"inputText"`
	// Dimensions and Normalize are optional; omit to use model defaults.
}

type titanResponse struct {
	Embedding       []float32 `json:"embedding"`
	InputTokenCount int       `json:"inputTextTokenCount"`
}

func (c *Client) embedTitan(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		body, err := json.Marshal(titanRequest{InputText: text})
		if err != nil {
			return nil, fmt.Errorf("bedrock embed: marshal titan request: %w", err)
		}

		out, err := c.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
			ModelId:     aws.String(c.model),
			ContentType: aws.String("application/json"),
			Body:        body,
		})
		if err != nil {
			return nil, fmt.Errorf("bedrock embed: invoke %s: %w", c.model, c.classifyBedrockError(err, c.model))
		}

		var resp titanResponse
		if err := json.Unmarshal(out.Body, &resp); err != nil {
			return nil, fmt.Errorf("bedrock embed: unmarshal titan response: %w", err)
		}
		results[i] = resp.Embedding
	}
	return results, nil
}

// --- Cohere Embed ---

type cohereRequest struct {
	Texts     []string `json:"texts"`
	InputType string   `json:"input_type"`
	Truncate  string   `json:"truncate"`
}

type cohereResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func (c *Client) embedCohere(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	// Cohere Bedrock supports up to 96 texts per call.
	const maxBatch = 96
	var all [][]float32

	for i := 0; i < len(texts); i += maxBatch {
		end := i + maxBatch
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		body, err := json.Marshal(cohereRequest{
			Texts:     batch,
			InputType: inputType,
			Truncate:  "END",
		})
		if err != nil {
			return nil, fmt.Errorf("bedrock embed: marshal cohere request: %w", err)
		}

		out, err := c.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
			ModelId:     aws.String(c.model),
			ContentType: aws.String("application/json"),
			Body:        body,
		})
		if err != nil {
			return nil, fmt.Errorf("bedrock embed: invoke %s: %w", c.model, c.classifyBedrockError(err, c.model))
		}

		var resp cohereResponse
		if err := json.Unmarshal(out.Body, &resp); err != nil {
			return nil, fmt.Errorf("bedrock embed: unmarshal cohere response: %w", err)
		}

		if len(resp.Embeddings) != len(batch) {
			return nil, fmt.Errorf("bedrock embed: expected %d embeddings, got %d", len(batch), len(resp.Embeddings))
		}
		all = append(all, resp.Embeddings...)
	}

	return all, nil
}

// --- helpers ---

func detectFamily(model string) (modelFamily, error) {
	switch {
	case strings.HasPrefix(model, "amazon.titan-embed"):
		return familyTitan, nil
	case strings.HasPrefix(model, "cohere.embed"):
		return familyCohere, nil
	default:
		return 0, fmt.Errorf("bedrock embed: unsupported model %q (expected amazon.titan-embed* or cohere.embed*)", model)
	}
}
