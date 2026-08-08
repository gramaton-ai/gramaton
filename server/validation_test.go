package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
)

func TestParseJSON(t *testing.T) {
	body := strings.NewReader(`{"name":"test","value":42}`)
	req, _ := http.NewRequest("POST", "/", body)
	req.Header.Set("Content-Type", "application/json")

	var result struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	}
	err := parseJSON(req, &result, getMaxJSONSize())
	if err != nil {
		t.Fatalf("parseJSON: %v", err)
	}
	if result.Name != "test" || result.Value != 42 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestParseJSONEmptyBody(t *testing.T) {
	body := strings.NewReader("")
	req, _ := http.NewRequest("POST", "/", body)
	err := parseJSON(req, &struct{}{}, getMaxJSONSize())
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty body error, got: %v", err)
	}
}

func TestParseJSONInvalidJSON(t *testing.T) {
	body := strings.NewReader("not json")
	req, _ := http.NewRequest("POST", "/", body)
	err := parseJSON(req, &struct{}{}, getMaxJSONSize())
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("expected invalid JSON error, got: %v", err)
	}
}

func TestParseJSONInvalidUTF8(t *testing.T) {
	body := strings.NewReader("{\"key\":\"\xff\"}")
	req, _ := http.NewRequest("POST", "/", body)
	err := parseJSON(req, &struct{}{}, getMaxJSONSize())
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("expected UTF-8 error, got: %v", err)
	}
}

func TestValidateFloat64RangeNil(t *testing.T) {
	err := validateFloat64Range("test", nil, 0, 1)
	if err != nil {
		t.Fatalf("nil should pass: %v", err)
	}
}

func TestValidateFloat64RangeValid(t *testing.T) {
	v := 0.5
	err := validateFloat64Range("test", &v, 0, 1)
	if err != nil {
		t.Fatalf("0.5 should be in range: %v", err)
	}
}

func TestValidateFloat64RangeOutOfRange(t *testing.T) {
	v := 2.0
	err := validateFloat64Range("test", &v, 0, 1)
	if err == nil {
		t.Fatal("2.0 should be out of range")
	}
}

func TestValidateEnumValid(t *testing.T) {
	err := validateEnum("test", "durable", validTemporalities)
	if err != nil {
		t.Fatalf("durable should be valid: %v", err)
	}
}

func TestValidateEnumInvalid(t *testing.T) {
	err := validateEnum("test", "bogus", validTemporalities)
	if err == nil {
		t.Fatal("bogus should be invalid")
	}
}

func TestValidateEnumEmpty(t *testing.T) {
	err := validateEnum("test", "", validTemporalities)
	if err != nil {
		t.Fatalf("empty should pass: %v", err)
	}
}

func TestServerLimits_ConfigDriven(t *testing.T) {
	// Save and restore the package-level limits so the test doesn't
	// leak state into other tests that run in the same binary.
	serverLimitsMu.RLock()
	prev := serverLimits
	serverLimitsMu.RUnlock()
	t.Cleanup(func() { setServerLimits(prev) })

	// Install test limits.
	setServerLimits(config.LimitsConfig{
		MaxSummaryShort: 50,
		MaxKeywords:     2,
		MaxJSONSize:     4096,
	})

	if got := getMaxSummaryShort(); got != 50 {
		t.Errorf("getMaxSummaryShort() = %d, want 50", got)
	}
	if got := getMaxKeywords(); got != 2 {
		t.Errorf("getMaxKeywords() = %d, want 2", got)
	}
	if got := getMaxJSONSize(); got != 4096 {
		t.Errorf("getMaxJSONSize() = %d, want 4096", got)
	}

	// Keyword count enforcement reads from the live limits.
	if err := validateKeywords([]string{"a", "b", "c"}); err == nil {
		t.Error("validateKeywords should reject 3 keywords when max=2")
	}
	if err := validateKeywords([]string{"a", "b"}); err != nil {
		t.Errorf("validateKeywords should accept 2 keywords when max=2: %v", err)
	}
}

func TestServerLimits_ZeroValueFallback(t *testing.T) {
	// A zero-value LimitsConfig means the YAML omitted the field; the
	// getters should fall back to safe defaults rather than enforcing 0.
	serverLimitsMu.RLock()
	prev := serverLimits
	serverLimitsMu.RUnlock()
	t.Cleanup(func() { setServerLimits(prev) })

	setServerLimits(config.LimitsConfig{}) // all zeros

	if got := getMaxSummaryShort(); got != 1000 {
		t.Errorf("getMaxSummaryShort() with zero config = %d, want 1000 fallback", got)
	}
	if got := getMaxKeywords(); got != 100 {
		t.Errorf("getMaxKeywords() with zero config = %d, want 100 fallback", got)
	}
	if got := getMaxJSONSize(); got != maxJSONBodySizeFallback {
		t.Errorf("getMaxJSONSize() with zero config = %d, want %d fallback", got, maxJSONBodySizeFallback)
	}
}
