package claudecli

import (
	"slices"
	"testing"

	"github.com/gramaton-ai/gramaton/internal/strutil"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// TestRunArgsDisablesToolsWithCurrentFlag pins the argv shape passed
// to the claude binary. The CLI's flag surface drifts between
// releases: the boolean `--no-allowedTools` was removed and every
// invocation using it failed with "error unknown option". The
// current CLI disables all built-in tools via `--tools ""`. This
// test fails if the dead flag reappears or the no-tools pair drifts.
func TestRunArgsDisablesToolsWithCurrentFlag(t *testing.T) {
	args := runArgs("sonnet")

	if slices.Contains(args, "--no-allowedTools") {
		t.Fatalf("args contain removed flag --no-allowedTools: %q", args)
	}

	i := slices.Index(args, "--tools")
	if i == -1 {
		t.Fatalf("args missing --tools: %q", args)
	}
	if i+1 >= len(args) || args[i+1] != "" {
		t.Fatalf("--tools must be followed by empty string to disable all tools, got: %q", args)
	}

	// The rest of the invocation shape: print mode, JSON output, model.
	for _, want := range []string{"-p", "--output-format", "json", "--model", "sonnet"} {
		if !slices.Contains(args, want) {
			t.Fatalf("args missing %q: %q", want, args)
		}
	}
}

func TestModelAliases(t *testing.T) {
	if modelAliases["haiku"] != "haiku" {
		t.Fatal("haiku alias wrong")
	}
	if modelAliases["sonnet"] != "sonnet" {
		t.Fatal("sonnet alias wrong")
	}
	if modelAliases["opus"] != "opus" {
		t.Fatal("opus alias wrong")
	}
}

// TestSumModelUsage verifies that the CLI's per-model token map is
// collapsed into the canonical telemetry.CallUsage shape. Proves the
// aggregation path: a pair of models both contributing to each field
// should sum field-by-field.
func TestSumModelUsage(t *testing.T) {
	m := map[string]cliModelUsageEntry{
		"claude-sonnet-4-6-20250514": {
			InputTokens: 100, OutputTokens: 20,
			CacheReadInputTokens: 50, CacheCreationInputTokens: 10,
		},
		"claude-haiku-4-5-20251001": {
			InputTokens: 33, OutputTokens: 7,
			CacheReadInputTokens: 5, CacheCreationInputTokens: 1,
		},
	}
	got := sumModelUsage(m)
	want := telemetry.CallUsage{
		InputTokens: 133, OutputTokens: 27,
		CacheReadTokens: 55, CacheWriteTokens: 11,
	}
	if got != want {
		t.Errorf("sumModelUsage = %+v, want %+v", got, want)
	}
}

// TestSumModelUsageEmpty confirms the degenerate case: no modelUsage
// block in the CLI response yields a zero CallUsage.
func TestSumModelUsageEmpty(t *testing.T) {
	if got := sumModelUsage(nil); got != (telemetry.CallUsage{}) {
		t.Errorf("sumModelUsage(nil) = %+v, want zero", got)
	}
}

func TestTruncateRedirectedToStrutil(t *testing.T) {
	if strutil.Truncate("hello", 10) != "hello" {
		t.Fatal("short string should not be truncated")
	}
	if strutil.Truncate("hello world", 5) != "hello..." {
		t.Fatal("long string should be truncated")
	}
}
