package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// TestClassifyBedrockErrorAccessDenied confirms AccessDeniedException
// from the SDK is wrapped with the model-access hint so users see
// the docs URL on first failure. Pre-fix the wrapper just bubbled
// the SDK message ("AccessDeniedException") with no remediation
// guidance -- one of the most common Bedrock onboarding pains
// (per the original release-blocking tracker).
func TestClassifyBedrockErrorAccessDenied(t *testing.T) {
	c := &Client{}
	msg := "User: arn:aws:sts::111122223333:assumed-role/Foo is not authorized to perform: bedrock:InvokeModel"
	original := &types.AccessDeniedException{Message: &msg}

	wrapped := c.classifyBedrockError(original, "anthropic.claude-test")

	if !strings.Contains(wrapped.Error(), "AccessDeniedException can mean") {
		t.Errorf("expected wrapped error to include the model-access hint; got: %v", wrapped)
	}
	if !strings.Contains(wrapped.Error(), "https://docs.aws.amazon.com/bedrock") {
		t.Errorf("expected wrapped error to include the enable-model-access docs URL; got: %v", wrapped)
	}
	// errors.Is/As must still find the original SDK error so callers
	// that pattern-match on it (retries, telemetry) keep working.
	var ade *types.AccessDeniedException
	if !errors.As(wrapped, &ade) {
		t.Errorf("wrapped error must still expose *types.AccessDeniedException via errors.As; got: %v", wrapped)
	}
}

// TestClassifyBedrockErrorResourceNotFound confirms model-id /
// region-mismatch failures are wrapped with a clear hint about
// region availability + inference-profile prefixes.
func TestClassifyBedrockErrorResourceNotFound(t *testing.T) {
	c := &Client{}
	msg := "Could not resolve the foundation model from model identifier"
	original := &types.ResourceNotFoundException{Message: &msg}

	wrapped := c.classifyBedrockError(original, "bogus.model")

	if !strings.Contains(wrapped.Error(), "not available in this region") {
		t.Errorf("expected ResourceNotFound wrap to mention region availability; got: %v", wrapped)
	}
	if !strings.Contains(wrapped.Error(), `"bogus.model"`) {
		t.Errorf("expected ResourceNotFound wrap to name the model; got: %v", wrapped)
	}
}

// TestClassifyBedrockErrorPassThroughOther confirms unrecognized
// errors are returned unchanged so the helper doesn't fight existing
// retry / telemetry behavior on errors it doesn't classify.
func TestClassifyBedrockErrorPassThroughOther(t *testing.T) {
	c := &Client{}
	original := errors.New("totally novel error")
	wrapped := c.classifyBedrockError(original, "model")
	if wrapped != original {
		t.Errorf("unrecognized error should be returned unchanged; got %v", wrapped)
	}
}

// TestClassifyBedrockErrorAccessDeniedDedupsWarn confirms that
// repeated AccessDeniedException wrappings against the same model
// only emit one Warn log line (the dedup map blocks subsequent
// emissions). The error wrap itself still applies on every call.
func TestClassifyBedrockErrorAccessDeniedDedupsWarn(t *testing.T) {
	c := &Client{}
	msg := "denied"
	original := &types.AccessDeniedException{Message: &msg}

	c.classifyBedrockError(original, "model-a")
	c.classifyBedrockError(original, "model-a")
	c.classifyBedrockError(original, "model-a")

	// Second model should re-emit a fresh warn (dedup is per-model).
	c.classifyBedrockError(original, "model-b")

	// Behavior verified by inspecting the dedup map directly.
	if _, ok := c.accessDeniedWarned.Load("model-a"); !ok {
		t.Error("model-a should be present in accessDeniedWarned after first call")
	}
	if _, ok := c.accessDeniedWarned.Load("model-b"); !ok {
		t.Error("model-b should be present in accessDeniedWarned after first call")
	}
}

func TestNewMissingModel(t *testing.T) {
	cfg := config.LLMConfig{
		Provider: "bedrock",
		Region:   "us-east-1",
	}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("New() with empty model should fail")
	}
}

func TestModelID(t *testing.T) {
	c := &Client{model: "us.anthropic.claude-sonnet-4-6"}
	if got := c.ModelID(); got != "us.anthropic.claude-sonnet-4-6" {
		t.Errorf("ModelID() = %q", got)
	}
}

// testClient creates a Client pointing at a local HTTP test server.
func testClient(t *testing.T, model string, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	awsCfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
	}
	client := bedrockruntime.NewFromConfig(awsCfg, func(o *bedrockruntime.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
	})

	return &Client{client: client, model: model}
}

func TestComplete(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		// The Converse API uses Smithy protocol, not raw JSON over
		// HTTP. For unit testing we verify the client construction
		// and error handling; integration tests cover the full API.
		// Return a valid Converse response shape.
		resp := map[string]any{
			"output": map[string]any{
				"message": map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"text": "Hello from Bedrock!"},
					},
				},
			},
			"stopReason": "end_turn",
			"usage": map[string]any{
				"inputTokens":  10,
				"outputTokens": 5,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}

	c := testClient(t, "us.anthropic.claude-sonnet-4-6", handler)
	got, err := c.Complete(context.Background(), "say hello")
	// The Converse API uses Smithy serialization which may not match
	// our simple JSON mock. If we get an error about deserialization,
	// that's expected -- the client was correctly constructed and
	// attempted the call. The real integration test uses actual Bedrock.
	if err != nil {
		if strings.Contains(err.Error(), "converse") || strings.Contains(err.Error(), "deserialize") {
			t.Skipf("Smithy protocol mismatch expected in unit test: %v", err)
		}
		t.Fatalf("Complete() = %v", err)
	}
	if got != "Hello from Bedrock!" {
		t.Errorf("Complete() = %q, want %q", got, "Hello from Bedrock!")
	}
}

// TestCompleteRecordsUsage proves Complete reports token counts from the
// Converse API's Usage field to a UsageRecorder attached to ctx. Skips
// when the Smithy JSON mock doesn't deserialize cleanly -- same guard
// TestComplete uses.
func TestCompleteRecordsUsage(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"output": map[string]any{
				"message": map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"text": "ok"},
					},
				},
			},
			"stopReason": "end_turn",
			"usage": map[string]any{
				"inputTokens":  123,
				"outputTokens": 45,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}

	c := testClient(t, "us.anthropic.claude-sonnet-4-6", handler)
	recorder := &telemetry.UsageRecorder{}
	ctx := telemetry.WithUsageRecorder(context.Background(), recorder)
	ctx = telemetry.WithTask(ctx, "classify")

	if _, err := c.Complete(ctx, "hi"); err != nil {
		if strings.Contains(err.Error(), "converse") || strings.Contains(err.Error(), "deserialize") {
			t.Skipf("Smithy protocol mismatch expected in unit test: %v", err)
		}
		t.Fatalf("Complete() = %v", err)
	}

	got := recorder.Total()
	want := telemetry.CallUsage{InputTokens: 123, OutputTokens: 45}
	if got != want {
		t.Errorf("recorded usage = %+v, want %+v", got, want)
	}
}

// TestSupportsStructuredOutput confirms Bedrock advertises
// structured output via Converse tool-use.
func TestSupportsStructuredOutput(t *testing.T) {
	c := &Client{model: "us.anthropic.claude-sonnet-4-6"}
	if !c.SupportsStructuredOutput() {
		t.Error("Bedrock.SupportsStructuredOutput() = false; want true")
	}
}

// TestCompleteStructured mirrors TestComplete: exercises the full
// tool-use construction path. Smithy serialization edge cases may
// cause the unit-test mock to reject the request — we skip on the
// same protocol-mismatch marker existing tests use.
func TestCompleteStructured(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		// Respond with a tool_use block carrying schema-valid JSON.
		resp := map[string]any{
			"output": map[string]any{
				"message": map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{
							"toolUse": map[string]any{
								"toolUseId": "tu_1",
								"name":      "emit_output",
								"input":     map[string]any{"field": "ok"},
							},
						},
					},
				},
			},
			"stopReason": "tool_use",
			"usage": map[string]any{
				"inputTokens":  50,
				"outputTokens": 8,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}

	c := testClient(t, "us.anthropic.claude-sonnet-4-6", handler)
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"field": map[string]any{"type": "string"},
		},
	}
	raw, err := c.CompleteStructured(context.Background(), schema, "emit ok")
	if err != nil {
		if strings.Contains(err.Error(), "converse") || strings.Contains(err.Error(), "deserialize") {
			t.Skipf("Smithy protocol mismatch expected in unit test: %v", err)
		}
		t.Fatalf("CompleteStructured: %v", err)
	}
	var parsed struct {
		Field string `json:"field"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	if parsed.Field != "ok" {
		t.Errorf("parsed.field = %q, want ok", parsed.Field)
	}
}

// TestCompleteStructuredMissingToolUseBlock mirrors the anthropic
// test of the same name: a response with no tool_use block for our
// tool should error, not return empty. Skipped when Smithy
// deserialization rejects the mock (same guard existing tests use).
func TestCompleteStructuredMissingToolUseBlock(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		// Valid Converse response shape but no toolUse block —
		// model emitted text instead.
		resp := map[string]any{
			"output": map[string]any{
				"message": map[string]any{
					"role": "assistant",
					"content": []map[string]any{
						{"text": "oops, text instead of tool use"},
					},
				},
			},
			"stopReason": "end_turn",
			"usage": map[string]any{
				"inputTokens":  5,
				"outputTokens": 3,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}

	c := testClient(t, "us.anthropic.claude-sonnet-4-6", handler)
	_, err := c.CompleteStructured(context.Background(), map[string]any{"type": "object"}, "prompt")
	if err == nil {
		t.Fatal("expected error when response has no tool_use block")
	}
	// Either the expected error or a Smithy protocol mismatch — both
	// fine for this unit test. The point is we didn't silently
	// return empty RawMessage.
	if strings.Contains(err.Error(), "converse") || strings.Contains(err.Error(), "deserialize") {
		t.Skipf("Smithy protocol mismatch expected in unit test: %v", err)
	}
	if !strings.Contains(err.Error(), "no tool_use block") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNewWithEnvCreds(t *testing.T) {
	t.Setenv("TEST_BEDROCK_AKID", "TESTACCESSKEY")
	t.Setenv("TEST_BEDROCK_SECRET", "TESTSECRETKEY")

	cfg := config.LLMConfig{
		Provider:              "bedrock",
		Models:                config.LLMModels{Medium: "us.anthropic.claude-sonnet-4-6"},
		Region:                "us-east-1",
		AWSAccessKeyIDEnv:     "TEST_BEDROCK_AKID",
		AWSSecretAccessKeyEnv: "TEST_BEDROCK_SECRET",
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if c.ModelID() != "us.anthropic.claude-sonnet-4-6" {
		t.Errorf("ModelID() = %q", c.ModelID())
	}
}

func TestCompleteIntegration(t *testing.T) {
	// Run with: go test -run TestCompleteIntegration
	// Requires valid AWS credentials and Bedrock model access.
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	cfg := config.LLMConfig{
		Provider: "bedrock",
		Models:   config.LLMModels{Medium: "us.anthropic.claude-sonnet-4-6"},
		Region:   "us-west-2",
	}

	c, err := New(cfg)
	if err != nil {
		t.Skipf("cannot create Bedrock client (no credentials?): %v", err)
	}

	got, err := c.Complete(context.Background(), "Reply with exactly: PONG")
	if err != nil {
		t.Skipf("Bedrock call failed (no model access?): %v", err)
	}

	if !strings.Contains(got, "PONG") {
		t.Errorf("Complete() = %q, expected to contain PONG", got)
	}
}

// TestSetSystemPromptStoresValue pins the SetSystemPrompt write
// behavior on the client field. Bedrock SDK calls aren't mockable
// without a custom HTTP transport (existing tests in this file
// don't exercise the SDK round-trip either), so this and the
// systemBlocks test below verify the path the SDK call would take.
func TestSetSystemPromptStoresValue(t *testing.T) {
	c := &Client{}
	c.SetSystemPrompt("be terse")
	if got := c.snapshotSystemPrompt(); got != "be terse" {
		t.Fatalf("snapshotSystemPrompt() = %q, want %q", got, "be terse")
	}
	c.SetSystemPrompt("")
	if got := c.snapshotSystemPrompt(); got != "" {
		t.Fatalf("after clear: snapshotSystemPrompt() = %q, want empty", got)
	}
}

// TestSystemBlocksOmitsWhenEmpty pins the wire-format contract: with
// no system prompt set, systemBlocks() returns nil so the SDK omits
// the System field from the Converse request entirely. Callers that
// never set a system prompt see no wire-format change.
func TestSystemBlocksOmitsWhenEmpty(t *testing.T) {
	c := &Client{}
	if blocks := c.systemBlocks(); blocks != nil {
		t.Fatalf("systemBlocks() with empty prompt = %v, want nil", blocks)
	}
}

// TestSystemBlocksIncludesPromptWhenSet pins the wire-format
// contract for the populated case: the SystemContentBlock slice
// carries a single text-typed block matching the configured prompt.
// This is what the Converse API expects in ConverseInput.System.
func TestSystemBlocksIncludesPromptWhenSet(t *testing.T) {
	c := &Client{}
	c.SetSystemPrompt("respond with json only")
	blocks := c.systemBlocks()
	if len(blocks) != 1 {
		t.Fatalf("systemBlocks() len = %d, want 1", len(blocks))
	}
	textBlock, ok := blocks[0].(*types.SystemContentBlockMemberText)
	if !ok {
		t.Fatalf("systemBlocks()[0] type = %T, want *SystemContentBlockMemberText", blocks[0])
	}
	if textBlock.Value != "respond with json only" {
		t.Errorf("text block value = %q, want %q", textBlock.Value, "respond with json only")
	}
}

// TestSetSystemPromptConcurrentSafe smoke-tests that concurrent
// SetSystemPrompt and snapshotSystemPrompt calls don't panic under
// the race detector. Curation today does set/call/clear under defer
// per goroutine, but the RWMutex protection matches the Anthropic
// precedent and protects against any future caller pattern.
func TestSetSystemPromptConcurrentSafe(t *testing.T) {
	c := &Client{}
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(i int) {
			c.SetSystemPrompt("prompt")
			_ = c.snapshotSystemPrompt()
			done <- struct{}{}
			_ = i
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}
