package llm

import (
	"log/slog"
	"strings"
	"sync"

	"github.com/gramaton-ai/gramaton/llm/telemetry"
)

// ModelPricing holds per-million-token prices (USD) for a single model.
// Caches have distinct rates: cache-read is billed at a steep discount,
// cache-write at a small premium. InputPerMtok is the standard rate for
// uncached input tokens.
type ModelPricing struct {
	InputPerMtok      float64 // uncached input
	OutputPerMtok     float64 // output
	CacheReadPerMtok  float64 // cache-hit input (typically ~10% of input)
	CacheWritePerMtok float64 // cache-write input (typically ~125% of input)
}

// modelPricing maps model-name prefixes to pricing. Lookup uses prefix
// match so version suffixes work without a table update. Rates are USD
// per million tokens, reflecting public provider pricing as of 2026.
// Update this table when providers change rates.
var modelPricing = []struct {
	prefix  string
	pricing ModelPricing
}{
	// Anthropic Claude.
	{"claude-opus-4", ModelPricing{
		InputPerMtok: 15, OutputPerMtok: 75,
		CacheReadPerMtok: 1.50, CacheWritePerMtok: 18.75,
	}},
	// Claude 3 used the "claude-3-{tier}-..." naming convention
	// (e.g. claude-3-opus-20240229). The "claude-{tier}-3" prefix
	// matches nothing in real API IDs -- left as a separate entry
	// rather than the inverted Claude-4 form so future readers can
	// see the naming-convention shift between generations.
	{"claude-3-opus", ModelPricing{
		InputPerMtok: 15, OutputPerMtok: 75,
		CacheReadPerMtok: 1.50, CacheWritePerMtok: 18.75,
	}},
	{"claude-sonnet-4", ModelPricing{
		InputPerMtok: 3, OutputPerMtok: 15,
		CacheReadPerMtok: 0.30, CacheWritePerMtok: 3.75,
	}},
	{"claude-3-sonnet", ModelPricing{
		InputPerMtok: 3, OutputPerMtok: 15,
		CacheReadPerMtok: 0.30, CacheWritePerMtok: 3.75,
	}},
	{"claude-3-5-sonnet", ModelPricing{
		InputPerMtok: 3, OutputPerMtok: 15,
		CacheReadPerMtok: 0.30, CacheWritePerMtok: 3.75,
	}},
	{"claude-haiku-4", ModelPricing{
		InputPerMtok: 0.80, OutputPerMtok: 4,
		CacheReadPerMtok: 0.08, CacheWritePerMtok: 1.00,
	}},
	{"claude-3-haiku", ModelPricing{
		InputPerMtok: 0.25, OutputPerMtok: 1.25,
		CacheReadPerMtok: 0.03, CacheWritePerMtok: 0.30,
	}},
	{"claude-3-5-haiku", ModelPricing{
		InputPerMtok: 0.80, OutputPerMtok: 4,
		CacheReadPerMtok: 0.08, CacheWritePerMtok: 1.00,
	}},
	// OpenAI.
	{"gpt-4o-mini", ModelPricing{
		InputPerMtok: 0.15, OutputPerMtok: 0.60,
		CacheReadPerMtok: 0.075, CacheWritePerMtok: 0.15,
	}},
	{"gpt-4o", ModelPricing{
		InputPerMtok: 2.50, OutputPerMtok: 10,
		CacheReadPerMtok: 1.25, CacheWritePerMtok: 2.50,
	}},
}

// pricingMissWarned tracks models we've already warned about so a
// hot-path call site doesn't log on every invocation.
var pricingMissWarned sync.Map

// LookupPricing returns pricing for the given model name via prefix
// match. Returns the zero value (all rates 0) when the model is
// unknown -- the cost calculator treats that as "no cost data". Logs
// a one-shot Warn per unknown model so operators notice when a new
// model lands without a pricing entry (cost dashboards would silently
// read zero otherwise).
func LookupPricing(model string) ModelPricing {
	for _, m := range modelPricing {
		if strings.HasPrefix(model, m.prefix) {
			return m.pricing
		}
	}
	if model != "" {
		if _, loaded := pricingMissWarned.LoadOrStore(model, struct{}{}); !loaded {
			slog.Warn("LLM pricing miss; cost will read as $0",
				"component", "llm",
				"model", model,
				"hint", "add a pricing entry in llm/pricing.go for this model")
		}
	}
	return ModelPricing{}
}

// EstimateCost computes the USD cost of a single LLM call given its
// token counts and model. Returns 0 for models not in modelPricing.
// InputTokens must be the TOTAL input count including cache read/write;
// this function subtracts those portions and applies the appropriate
// rate to each.
func EstimateCost(model string, u telemetry.CallUsage) float64 {
	p := LookupPricing(model)
	uncachedInput := u.InputTokens - u.CacheReadTokens - u.CacheWriteTokens
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	return float64(uncachedInput)*p.InputPerMtok/1e6 +
		float64(u.CacheReadTokens)*p.CacheReadPerMtok/1e6 +
		float64(u.CacheWriteTokens)*p.CacheWritePerMtok/1e6 +
		float64(u.OutputTokens)*p.OutputPerMtok/1e6
}
