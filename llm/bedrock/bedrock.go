package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/awscfg"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// modelAccessHint is appended to AccessDeniedException errors. The
// same exception covers two distinct failure modes -- IAM permission
// missing and "model access not granted in console" -- and the SDK
// message often reads "AccessDeniedException" with a generic body.
// Surfacing both possibilities up front saves a long debugging
// session against AWS console settings.
const modelAccessHint = "AccessDeniedException can mean (1) the IAM principal lacks bedrock:InvokeModel for this model, OR (2) the model has not been enabled in the Bedrock console for this account/region. To enable model access: https://docs.aws.amazon.com/bedrock/latest/userguide/model-access-modify.html"

// classifyBedrockError wraps SDK errors with hints for the most
// common Bedrock onboarding failures. AccessDeniedException is by
// far the loudest one; ResourceNotFoundException usually means a
// model-ID typo or the model is not available in the configured
// region. Only logs the hint once per (errType, model) pair via a
// dedup map so a tight curation loop doesn't repeat the same line.
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
		return fmt.Errorf("%w (hint: model %q is not available in this region; check region setting and the model-id format -- inference profiles use prefixes like us.anthropic.…)", err, model)
	}
	return err
}

// warnAccessDenied dedup-warns once per model so a curation loop
// hammering a non-enabled model doesn't flood the log.
func (c *Client) warnAccessDenied(model string) {
	if _, loaded := c.accessDeniedWarned.LoadOrStore(model, struct{}{}); loaded {
		return
	}
	slog.Warn("bedrock: AccessDeniedException -- check model access + IAM",
		"component", "llm",
		"model", model,
		"hint", modelAccessHint)
}

// Client calls the Bedrock Converse API for LLM completions. The
// Converse API is model-agnostic -- it works with Claude, Titan,
// Llama, Mistral, and any other model available on Bedrock.
type Client struct {
	client *bedrockruntime.Client
	model  string

	// ignoredModelWarned dedups the per-override Warn from
	// CompleteWithModel so a tight curation loop doesn't flood logs.
	ignoredModelWarned sync.Map

	// accessDeniedWarned dedups the per-model AccessDeniedException
	// hint so a curation loop hammering a non-enabled model doesn't
	// repeat the same line every cycle.
	accessDeniedWarned sync.Map
}

// New creates a Bedrock LLM client from the LLM config.
//
// Logs at Info on successful construction with the resolved
// (region, profile, model) -- one line per provider lifetime, so
// the daemon log carries an audit trail of which AWS principal
// surface this process is using. Credential values themselves are
// never logged; per-call refresh / retry diagnostics flow through
// the awscfg slog adapter at Debug.
func New(cfg config.LLMConfig) (*Client, error) {
	// Default model used by Complete() (no explicit model). Most call
	// sites pass a model via CompleteWithModel resolved through
	// cfg.ModelForTask; this only fires for callers that don't.
	model := cfg.Models.Medium
	if model == "" {
		return nil, fmt.Errorf("bedrock llm: cfg.LLM.Models.Medium is required")
	}

	awsCfg, err := awscfg.Load(context.Background(), cfg.Region, cfg.AWSProfile,
		cfg.AWSAccessKeyIDEnv, cfg.AWSSecretAccessKeyEnv)
	if err != nil {
		return nil, fmt.Errorf("bedrock llm: load AWS config: %w", err)
	}

	slog.Info("bedrock llm: client initialized",
		"component", "llm",
		"region", cfg.Region,
		"profile_set", cfg.AWSProfile != "",
		"model", model)

	return &Client{
		client: bedrockruntime.NewFromConfig(awsCfg),
		model:  model,
	}, nil
}

// CompleteWithModel ignores the model override (Bedrock uses a fixed
// endpoint per model). Logs a one-shot Warn per distinct override
// so callers expecting cross-provider consistency notice that this
// provider treats its model as fixed at construction.
func (c *Client) CompleteWithModel(ctx context.Context, model, prompt string) (string, error) {
	if model != "" && model != c.model {
		c.warnIgnoredModel(model)
	}
	return c.Complete(ctx, prompt)
}

// warnIgnoredModel deduplicates the per-override warning so a tight
// curation loop doesn't flood the logs with the same line.
func (c *Client) warnIgnoredModel(model string) {
	if _, loaded := c.ignoredModelWarned.LoadOrStore(model, struct{}{}); loaded {
		return
	}
	slog.Warn("bedrock: ignoring CompleteWithModel override",
		"component", "llm",
		"requested", model,
		"using", c.model,
		"hint", "bedrock client uses the model fixed at construction; configure llm.model or llm.models.* to switch")
}

// Complete sends a prompt via the Converse API and returns the text.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	out, err := c.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String(c.model),
		Messages: []types.Message{
			{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					&types.ContentBlockMemberText{Value: prompt},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("bedrock llm: converse %s: %w", c.model, c.classifyBedrockError(err, c.model))
	}

	// Extract text from the response message.
	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return "", fmt.Errorf("bedrock llm: unexpected output type")
	}

	var text string
	for _, block := range msg.Value.Content {
		if tb, ok := block.(*types.ContentBlockMemberText); ok {
			text += tb.Value
		}
	}

	// Converse API reports total input/output tokens. Cache accounting
	// isn't surfaced here, so cache fields stay zero for this provider.
	if out.Usage != nil {
		var input, output int32
		if out.Usage.InputTokens != nil {
			input = *out.Usage.InputTokens
		}
		if out.Usage.OutputTokens != nil {
			output = *out.Usage.OutputTokens
		}
		telemetry.Record(ctx, telemetry.CallUsage{
			InputTokens:  int(input),
			OutputTokens: int(output),
		})
	}

	return text, nil
}

// ModelID returns the Bedrock model identifier.
func (c *Client) ModelID() string {
	return c.model
}

// ProviderName returns the identifier used in per-provider metrics.
func (c *Client) ProviderName() string { return "bedrock" }

// SupportsStructuredOutput reports true. The Converse API accepts a
// toolConfig regardless of model; Claude models enforce the schema
// at the API edge the same way the direct Anthropic API does. Other
// models (Titan, Llama, Mistral) also accept toolConfig via Converse
// but may not enforce strictly — if a non-supporting model is
// configured, CompleteStructured will return an error and the
// caller's fallback to Complete kicks in.
func (c *Client) SupportsStructuredOutput() bool { return true }

// CompleteStructured sends the prompt with a toolConfig that forces
// a single "emit_output" tool whose InputSchema is the caller's
// JSON Schema. The response contains a toolUse block whose Input is
// the schema-valid JSON (for models that honor tool-use, Claude on
// Bedrock being the primary supported case). Returns the raw JSON
// bytes so the caller can Unmarshal into a typed struct.
func (c *Client) CompleteStructured(ctx context.Context, schema map[string]any, prompt string) (json.RawMessage, error) {
	// Bedrock's aws-sdk-go-v2 Converse types use types.ToolInputSchema
	// with a document-typed Json field. document.NewLazyDocument
	// converts the Go map into the opaque Document type the SDK
	// passes to Bedrock.
	toolName := "emit_output"
	out, err := c.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String(c.model),
		Messages: []types.Message{{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: prompt},
			},
		}},
		ToolConfig: &types.ToolConfiguration{
			Tools: []types.Tool{
				&types.ToolMemberToolSpec{
					Value: types.ToolSpecification{
						Name:        aws.String(toolName),
						Description: aws.String("Emit the structured output."),
						InputSchema: &types.ToolInputSchemaMemberJson{
							Value: document.NewLazyDocument(schema),
						},
					},
				},
			},
			ToolChoice: &types.ToolChoiceMemberTool{
				Value: types.SpecificToolChoice{
					Name: aws.String(toolName),
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock structured: converse %s: %w", c.model, c.classifyBedrockError(err, c.model))
	}

	// Record usage identically to Complete.
	if out.Usage != nil {
		var input, output int32
		if out.Usage.InputTokens != nil {
			input = *out.Usage.InputTokens
		}
		if out.Usage.OutputTokens != nil {
			output = *out.Usage.OutputTokens
		}
		telemetry.Record(ctx, telemetry.CallUsage{
			InputTokens:  int(input),
			OutputTokens: int(output),
		})
	}

	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return nil, fmt.Errorf("bedrock structured: unexpected output type")
	}

	// Find the tool_use block for our tool and unmarshal its Input
	// (a document.Interface holding the schema-valid JSON) back to
	// raw JSON bytes.
	for _, block := range msg.Value.Content {
		tu, ok := block.(*types.ContentBlockMemberToolUse)
		if !ok {
			continue
		}
		if aws.ToString(tu.Value.Name) != toolName {
			continue
		}
		raw, err := tu.Value.Input.MarshalSmithyDocument()
		if err != nil {
			return nil, fmt.Errorf("bedrock structured: marshal tool input: %w", err)
		}
		return raw, nil
	}
	return nil, fmt.Errorf("bedrock structured: response had no tool_use block for %s", toolName)
}
