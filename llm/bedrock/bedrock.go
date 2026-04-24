package bedrock

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/awscfg"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// Client calls the Bedrock Converse API for LLM completions. The
// Converse API is model-agnostic -- it works with Claude, Titan,
// Llama, Mistral, and any other model available on Bedrock.
type Client struct {
	client *bedrockruntime.Client
	model  string
}

// New creates a Bedrock LLM client from the LLM config.
func New(cfg config.LLMConfig) (*Client, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("bedrock llm: model is required")
	}

	awsCfg, err := awscfg.Load(context.Background(), cfg.Region, cfg.AWSProfile,
		cfg.AWSAccessKeyIDEnv, cfg.AWSSecretAccessKeyEnv)
	if err != nil {
		return nil, fmt.Errorf("bedrock llm: load AWS config: %w", err)
	}

	return &Client{
		client: bedrockruntime.NewFromConfig(awsCfg),
		model:  cfg.Model,
	}, nil
}

// CompleteWithModel ignores the model override (Bedrock uses a fixed
// endpoint per model). Falls back to the configured model.
func (c *Client) CompleteWithModel(ctx context.Context, model, prompt string) (string, error) {
	return c.Complete(ctx, prompt)
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
		return "", fmt.Errorf("bedrock llm: converse %s: %w", c.model, err)
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
		return nil, fmt.Errorf("bedrock structured: converse %s: %w", c.model, err)
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
