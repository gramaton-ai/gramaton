package llm

import (
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

func TestNewEmptyProvider(t *testing.T) {
	cfg := config.LLMConfig{Provider: ""}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New empty: %v", err)
	}
	if p != nil {
		t.Fatal("expected nil provider for empty config")
	}
}

func TestNewAnthropicProvider(t *testing.T) {
	cfg := config.LLMConfig{
		Provider:  "anthropic",
		Models:     config.LLMModels{Medium:"claude-sonnet-4-6"},
		APIKeyEnv: "sk-ant-test-key",
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New anthropic: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if p.ModelID() != "claude-sonnet-4-6" {
		t.Fatalf("expected model 'claude-sonnet-4-6', got %q", p.ModelID())
	}
}

func TestNewAnthropicNoKey(t *testing.T) {
	cfg := config.LLMConfig{
		Provider:  "anthropic",
		APIKeyEnv: "NONEXISTENT_KEY_99999",
	}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected error when API key is missing")
	}
}

func TestNewUnknownProvider(t *testing.T) {
	cfg := config.LLMConfig{Provider: "unknown"}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}
