package bedrock

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
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

// SupportsStructuredOutput reports false for now. Bedrock via
// Converse API tool-use would enable this for Claude-family models;
// implementation is deferred to a follow-up commit. Non-Claude
// models under Bedrock have no structured-output equivalent.
func (c *Client) SupportsStructuredOutput() bool { return false }

// CompleteStructured is not yet implemented for Bedrock.
func (c *Client) CompleteStructured(_ context.Context, _ map[string]any, _ string) (json.RawMessage, error) {
	return nil, fmt.Errorf("bedrock: structured output not yet implemented")
}
