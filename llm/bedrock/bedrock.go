package bedrock

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/internal/awscfg"
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

	return text, nil
}

// ModelID returns the Bedrock model identifier.
func (c *Client) ModelID() string {
	return c.model
}
