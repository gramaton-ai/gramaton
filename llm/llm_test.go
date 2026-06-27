package llm

import (
	"context"
	"encoding/json"
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
		Models:    config.LLMModels{Medium: "claude-sonnet-4-6"},
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

// fakeProvider is a minimal Provider for Unwrap tests. It does NOT
// implement SystemPromptSetter -- tests use it to verify that
// type assertion against the unwrapped chain returns ok=false when
// the actual implementation doesn't exist, which is the core
// contract Unwrap is meant to expose.
type fakeProvider struct{ id string }

func (p fakeProvider) Complete(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (p fakeProvider) CompleteWithModel(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (p fakeProvider) ModelID() string                { return p.id }
func (p fakeProvider) ProviderName() string           { return "fake" }
func (p fakeProvider) SupportsStructuredOutput() bool { return false }
func (p fakeProvider) CompleteStructured(_ context.Context, _ map[string]any, _ string) (json.RawMessage, error) {
	return nil, nil
}

// fakeSystemPromptProvider extends fakeProvider with a SetSystemPrompt
// method so tests can verify the unwrap-and-assert pattern returns the
// inner implementation when present.
type fakeSystemPromptProvider struct {
	fakeProvider
	setCalls int
	lastText string
}

func (p *fakeSystemPromptProvider) SetSystemPrompt(text string) {
	p.setCalls++
	p.lastText = text
}

// fakeWrapper is a minimal Provider wrapper exposing Inner(). Used to
// verify Unwrap recurses through arbitrary wrapper stacks (Metered
// today; potentially RateLimited(Metered(...)) tomorrow).
type fakeWrapper struct {
	inner Provider
}

func (w *fakeWrapper) Complete(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (w *fakeWrapper) CompleteWithModel(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (w *fakeWrapper) ModelID() string                { return w.inner.ModelID() }
func (w *fakeWrapper) ProviderName() string           { return "wrapper" }
func (w *fakeWrapper) SupportsStructuredOutput() bool { return w.inner.SupportsStructuredOutput() }
func (w *fakeWrapper) CompleteStructured(_ context.Context, _ map[string]any, _ string) (json.RawMessage, error) {
	return nil, nil
}
func (w *fakeWrapper) Inner() Provider { return w.inner }

// fakeLyingWrapper mirrors the pre-fix Metered.SetSystemPrompt shape:
// declares the capability method on itself (so direct type assertion
// succeeds at the type-assertion layer) AND exposes Inner() to a
// possibly-non-implementing inner. This is the exact pattern the
// Unwrap fix is designed to expose -- without Unwrap, a raw
// `provider.(SystemPromptSetter)` returns ok=true on this type
// regardless of inner support, producing silent capability lies.
type fakeLyingWrapper struct {
	inner Provider
}

func (w *fakeLyingWrapper) Complete(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (w *fakeLyingWrapper) CompleteWithModel(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (w *fakeLyingWrapper) ModelID() string                { return w.inner.ModelID() }
func (w *fakeLyingWrapper) ProviderName() string           { return "lying-wrapper" }
func (w *fakeLyingWrapper) SupportsStructuredOutput() bool { return w.inner.SupportsStructuredOutput() }
func (w *fakeLyingWrapper) CompleteStructured(_ context.Context, _ map[string]any, _ string) (json.RawMessage, error) {
	return nil, nil
}
func (w *fakeLyingWrapper) Inner() Provider { return w.inner }

// SetSystemPrompt declares the capability on the wrapper itself
// (the lie). Delegates to inner only when inner implements;
// silently swallows the call otherwise.
func (w *fakeLyingWrapper) SetSystemPrompt(text string) {
	if s, ok := w.inner.(SystemPromptSetter); ok {
		s.SetSystemPrompt(text)
	}
}

// TestUnwrapNoWrapper: Unwrap on a Provider without Inner() returns
// the same Provider. Pins the idempotent contract.
func TestUnwrapNoWrapper(t *testing.T) {
	p := fakeProvider{id: "base"}
	got := Unwrap(p)
	if got.ModelID() != "base" {
		t.Fatalf("Unwrap on base provider: ModelID = %q, want base", got.ModelID())
	}
}

// TestUnwrapSingleWrapper: Unwrap walks one level through Inner()
// and returns the underlying base.
func TestUnwrapSingleWrapper(t *testing.T) {
	base := fakeProvider{id: "base"}
	wrapped := &fakeWrapper{inner: base}
	got := Unwrap(wrapped)
	if got.ModelID() != "base" {
		t.Fatalf("Unwrap single-wrapper: ModelID = %q, want base", got.ModelID())
	}
}

// TestUnwrapNestedWrappers: Unwrap recurses through arbitrary wrapper
// depth. Pins the contract that's load-bearing for any future
// multi-wrapper stack (e.g. Metered around RateLimited).
func TestUnwrapNestedWrappers(t *testing.T) {
	base := fakeProvider{id: "base"}
	doublyWrapped := &fakeWrapper{inner: &fakeWrapper{inner: base}}
	got := Unwrap(doublyWrapped)
	if got.ModelID() != "base" {
		t.Fatalf("Unwrap nested-wrapper: ModelID = %q, want base", got.ModelID())
	}
}

// TestUnwrapCapabilityDetectionRejectsLyingWrapper pins the
// load-bearing behavioral contract: when a wrapper declares a
// capability method on itself (satisfying the interface at the
// type-assertion layer) but inner doesn't actually implement it,
// Unwrap-then-assert must correctly return ok=false rather than
// trusting the wrapper's advertisement.
//
// First the test verifies the fixture is genuinely a lying
// wrapper (raw assertion succeeds against the wrapper). Then it
// verifies Unwrap-then-assert exposes the truth. Without Unwrap,
// the raw assertion returns ok=true and downstream code calls a
// no-op delegating method -- the bug class this PR addresses.
func TestUnwrapCapabilityDetectionRejectsLyingWrapper(t *testing.T) {
	// fakeProvider does NOT implement SystemPromptSetter.
	base := fakeProvider{id: "base"}
	wrapped := &fakeLyingWrapper{inner: base}

	// The fixture must genuinely lie: raw assertion against the
	// wrapper has to succeed for the bug class to exist.
	if _, ok := Provider(wrapped).(SystemPromptSetter); !ok {
		t.Fatal("test fixture broken: lying wrapper should satisfy interface directly via its declared method")
	}

	// Unwrap then assert correctly exposes that the unwrapped
	// inner doesn't actually implement.
	inner := Unwrap(wrapped)
	if _, ok := inner.(SystemPromptSetter); ok {
		t.Fatal("Unwrap then assert: ok=true against non-implementing inner; capability detection is lying")
	}
}

// TestUnwrapCapabilityDetectionFindsImplementingInner: when the
// inner DOES implement, Unwrap-then-assert should succeed and the
// returned setter should hit the inner's implementation.
func TestUnwrapCapabilityDetectionFindsImplementingInner(t *testing.T) {
	impl := &fakeSystemPromptProvider{fakeProvider: fakeProvider{id: "impl"}}
	wrapped := &fakeWrapper{inner: impl}

	inner := Unwrap(wrapped)
	setter, ok := inner.(SystemPromptSetter)
	if !ok {
		t.Fatal("Unwrap then assert: ok=false against implementing inner; capability detection missed it")
	}
	setter.SetSystemPrompt("hello")
	if impl.setCalls != 1 || impl.lastText != "hello" {
		t.Fatalf("setter did not hit inner: setCalls=%d lastText=%q", impl.setCalls, impl.lastText)
	}
}
