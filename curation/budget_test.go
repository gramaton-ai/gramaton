package curation

import (
	"context"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// TestCycleBudgetExceededCount verifies the count cap fires regardless
// of cost cap, and ignores the recorder entirely.
func TestCycleBudgetExceededCount(t *testing.T) {
	cfg := config.Defaults()
	result := &AutonomousResult{LLMCalls: 20}
	if !cycleBudgetExceeded(context.Background(), cfg, result, 20, 0) {
		t.Fatal("count cap at exact hit should return true")
	}

	result.LLMCalls = 19
	if cycleBudgetExceeded(context.Background(), cfg, result, 20, 0) {
		t.Fatal("count cap below threshold should return false")
	}
}

// TestCycleBudgetExceededCost verifies the cost cap reads per-task
// usage from the cycle recorder and compares against the USD threshold
// using the pricing table.
func TestCycleBudgetExceededCost(t *testing.T) {
	cfg := config.Defaults()
	cfg.LLM.Models.Medium = "claude-sonnet-4-6" // in pricing table

	recorder := &telemetry.UsageRecorder{}
	// 1M input tokens on Sonnet-tier (contradiction task) = $3.00 USD.
	recorder.Record("contradiction", telemetry.CallUsage{InputTokens: 1_000_000})
	ctx := telemetry.WithUsageRecorder(context.Background(), recorder)

	result := &AutonomousResult{LLMCalls: 5}
	// $1 cap -- $3 cost should trip.
	if !cycleBudgetExceeded(ctx, cfg, result, 1000, 1.0) {
		t.Fatal("cost cap at $1 with $3 spent should return true")
	}
	// $10 cap -- $3 cost should not trip.
	if cycleBudgetExceeded(ctx, cfg, result, 1000, 10.0) {
		t.Fatal("cost cap at $10 with $3 spent should return false")
	}
	// 0 cap disables cost check entirely.
	if cycleBudgetExceeded(ctx, cfg, result, 1000, 0) {
		t.Fatal("maxCostUSD=0 should disable cost check")
	}
}

// TestCycleBudgetExceededUnknownModel covers the backstop semantics:
// when the model isn't in the pricing table, cost reads as 0 and only
// the count cap can trip.
func TestCycleBudgetExceededUnknownModel(t *testing.T) {
	cfg := config.Defaults()
	cfg.LLM.Models.Medium = "fictional-model-x" // not in pricing table

	recorder := &telemetry.UsageRecorder{}
	recorder.Record("contradiction", telemetry.CallUsage{InputTokens: 1_000_000_000})
	ctx := telemetry.WithUsageRecorder(context.Background(), recorder)

	result := &AutonomousResult{LLMCalls: 5}
	if cycleBudgetExceeded(ctx, cfg, result, 1000, 1.0) {
		t.Fatal("unknown model with $1 cap should not trip (cost reads as 0)")
	}
}
