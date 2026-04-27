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
)

// TestClassifyBedrockErrorAccessDenied (embed) -- mirrors the LLM
// client's test. Confirms AccessDeniedException is wrapped with the
// model-access hint AND the original SDK error survives errors.As.
func TestClassifyBedrockErrorAccessDenied(t *testing.T) {
	c := &Client{}
	msg := "denied"
	original := &types.AccessDeniedException{Message: &msg}
	wrapped := c.classifyBedrockError(original, "amazon.titan-embed-text-v2:0")

	if !strings.Contains(wrapped.Error(), "AccessDeniedException can mean") {
		t.Errorf("expected wrapped error to include the model-access hint; got: %v", wrapped)
	}
	var ade *types.AccessDeniedException
	if !errors.As(wrapped, &ade) {
		t.Errorf("wrapped error must still expose *types.AccessDeniedException via errors.As; got: %v", wrapped)
	}
}

// TestClassifyBedrockErrorResourceNotFound (embed) confirms the
// region-availability hint surfaces on ResourceNotFoundException.
func TestClassifyBedrockErrorResourceNotFound(t *testing.T) {
	c := &Client{}
	msg := "not found"
	original := &types.ResourceNotFoundException{Message: &msg}
	wrapped := c.classifyBedrockError(original, "bogus.embed.model")

	if !strings.Contains(wrapped.Error(), "not available in this region") {
		t.Errorf("expected wrap to mention region availability; got: %v", wrapped)
	}
}

// TestClassifyBedrockErrorPassThroughOther confirms unknown errors
// pass through untouched.
func TestClassifyBedrockErrorPassThroughOther(t *testing.T) {
	c := &Client{}
	original := errors.New("totally novel error")
	if got := c.classifyBedrockError(original, "model"); got != original {
		t.Errorf("unrecognized error should be returned unchanged; got %v", got)
	}
}

func TestDetectFamily(t *testing.T) {
	tests := []struct {
		model   string
		want    modelFamily
		wantErr bool
	}{
		{"amazon.titan-embed-text-v2:0", familyTitan, false},
		{"amazon.titan-embed-text-v1", familyTitan, false},
		{"cohere.embed-english-v3", familyCohere, false},
		{"cohere.embed-multilingual-v3", familyCohere, false},
		{"anthropic.claude-3-5-sonnet-20241022-v2:0", 0, true},
		{"", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got, err := detectFamily(tt.model)
			if (err != nil) != tt.wantErr {
				t.Fatalf("detectFamily(%q) error = %v, wantErr %v", tt.model, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("detectFamily(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestModelID(t *testing.T) {
	c := &Client{model: "amazon.titan-embed-text-v2:0", family: familyTitan}
	if got := c.ModelID(); got != "amazon.titan-embed-text-v2:0" {
		t.Errorf("ModelID() = %q, want %q", got, "amazon.titan-embed-text-v2:0")
	}
}

func TestEmbedEmpty(t *testing.T) {
	c := &Client{model: "amazon.titan-embed-text-v2:0", family: familyTitan}
	got, err := c.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("Embed(nil) = %v", err)
	}
	if got != nil {
		t.Errorf("Embed(nil) = %v, want nil", got)
	}
}

func TestNewUnsupportedModel(t *testing.T) {
	cfg := config.EmbeddingConfig{
		Provider: "bedrock",
		Model:    "meta.llama-3-8b",
		Region:   "us-east-1",
	}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("New() with unsupported model should fail")
	}
}

// testClient creates a Client pointing at a local HTTP test server,
// bypassing real AWS auth.
func testClient(t *testing.T, model string, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	family, err := detectFamily(model)
	if err != nil {
		t.Fatalf("detectFamily(%q): %v", model, err)
	}

	awsCfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
	}
	client := bedrockruntime.NewFromConfig(awsCfg, func(o *bedrockruntime.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
	})

	return &Client{client: client, model: model, family: family}
}

func TestEmbedTitan(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		var req titanRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		resp := titanResponse{
			Embedding:       []float32{0.1, 0.2, 0.3},
			InputTokenCount: 5,
		}
		json.NewEncoder(w).Encode(resp)
	}

	c := testClient(t, "amazon.titan-embed-text-v2:0", handler)
	got, err := c.Embed(context.Background(), []string{"hello world"})
	if err != nil {
		t.Fatalf("Embed() = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d embeddings, want 1", len(got))
	}
	if len(got[0]) != 3 {
		t.Errorf("embedding dim = %d, want 3", len(got[0]))
	}
}

func TestEmbedTitanMultiple(t *testing.T) {
	calls := 0
	handler := func(w http.ResponseWriter, r *http.Request) {
		calls++
		resp := titanResponse{
			Embedding:       []float32{0.1, 0.2},
			InputTokenCount: 3,
		}
		json.NewEncoder(w).Encode(resp)
	}

	c := testClient(t, "amazon.titan-embed-text-v2:0", handler)
	got, err := c.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Embed() = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d embeddings, want 3", len(got))
	}
	if calls != 3 {
		t.Errorf("Titan should call InvokeModel per text: got %d calls, want 3", calls)
	}
}

func TestEmbedCohere(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		var req cohereRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if req.InputType != "search_document" {
			t.Errorf("input_type = %q, want search_document", req.InputType)
		}
		if req.Truncate != "END" {
			t.Errorf("truncate = %q, want END", req.Truncate)
		}
		embs := make([][]float32, len(req.Texts))
		for i := range embs {
			embs[i] = []float32{0.1, 0.2}
		}
		resp := cohereResponse{Embeddings: embs}
		json.NewEncoder(w).Encode(resp)
	}

	c := testClient(t, "cohere.embed-english-v3", handler)
	got, err := c.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed() = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d embeddings, want 2", len(got))
	}
}

func TestEmbedCohereCountMismatch(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		// Return 1 embedding for 2 inputs.
		resp := cohereResponse{Embeddings: [][]float32{{0.1}}}
		json.NewEncoder(w).Encode(resp)
	}

	c := testClient(t, "cohere.embed-english-v3", handler)
	_, err := c.Embed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error on count mismatch")
	}
}

func TestNewWithEnvCreds(t *testing.T) {
	t.Setenv("TEST_BEDROCK_AKID", "TESTACCESSKEY")
	t.Setenv("TEST_BEDROCK_SECRET", "TESTSECRETKEY")

	cfg := config.EmbeddingConfig{
		Provider:              "bedrock",
		Model:                 "amazon.titan-embed-text-v2:0",
		Region:                "us-east-1",
		AWSAccessKeyIDEnv:     "TEST_BEDROCK_AKID",
		AWSSecretAccessKeyEnv: "TEST_BEDROCK_SECRET",
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if c.ModelID() != "amazon.titan-embed-text-v2:0" {
		t.Errorf("ModelID() = %q", c.ModelID())
	}
	if c.family != familyTitan {
		t.Errorf("family = %v, want familyTitan", c.family)
	}
}
