package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CallMetrics records telemetry for a single LLM call.
type CallMetrics struct {
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	Task             string  `json:"task"` // classify, summarize, contradiction, concept, manifest
	InputTokens      int     `json:"input_tokens,omitempty"`
	OutputTokens     int     `json:"output_tokens,omitempty"`
	CacheReadTokens  int     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int     `json:"cache_write_tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	Credits          float64 `json:"credits,omitempty"`
	LatencyMs        int64   `json:"latency_ms"`
	Success          bool    `json:"success"`
	ErrorType        string  `json:"error_type,omitempty"`
}

// UsageStats holds aggregated usage counters.
type UsageStats struct {
	StartedAt    time.Time      `json:"started_at"`
	Calls        int            `json:"calls"`
	ByTask       map[string]int `json:"by_task"`
	ByModel      map[string]int `json:"by_model"`
	InputTokens  int            `json:"input_tokens"`
	OutputTokens int            `json:"output_tokens"`
	CostUSD      float64        `json:"cost_usd"`
	Credits      float64        `json:"credits"`
	Errors       int            `json:"errors"`
	LatencySumMs int64          `json:"latency_sum_ms"` // for computing average
}

// UsageSummary is the full usage report returned by the API.
type UsageSummary struct {
	Session  UsageStats `json:"session"`
	Today    UsageStats `json:"today"`
	Lifetime UsageStats `json:"lifetime"`
}

// UsageTracker tracks LLM usage across sessions with cap enforcement.
type UsageTracker struct {
	mu sync.Mutex

	session  UsageStats
	today    UsageStats
	todayStr string // "2006-01-02" for day boundary detection
	lifetime UsageStats

	// Cap enforcement.
	maxCallsPerDay     int
	maxCallsPerSession int
	maxCostUSDPerDay   float64
	paused             bool
	pauseReason        string

	// Persistence.
	dataDir string
}

// NewUsageTracker creates a tracker, loading lifetime stats from disk.
// maxCostUSDPerDay = 0 disables the cost cap (count caps still apply).
func NewUsageTracker(dataDir string, maxPerDay, maxPerSession int, maxCostUSDPerDay float64) *UsageTracker {
	t := &UsageTracker{
		session: UsageStats{
			StartedAt: time.Now().UTC(),
			ByTask:    make(map[string]int),
			ByModel:   make(map[string]int),
		},
		today: UsageStats{
			StartedAt: time.Now().UTC(),
			ByTask:    make(map[string]int),
			ByModel:   make(map[string]int),
		},
		todayStr: time.Now().UTC().Format("2006-01-02"),
		lifetime: UsageStats{
			ByTask:  make(map[string]int),
			ByModel: make(map[string]int),
		},
		maxCallsPerDay:     maxPerDay,
		maxCallsPerSession: maxPerSession,
		maxCostUSDPerDay:   maxCostUSDPerDay,
		dataDir:            dataDir,
	}
	t.loadFromDisk()
	return t
}

// Record logs a single LLM call's metrics.
func (t *UsageTracker) Record(m CallMetrics) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Reset daily stats on day boundary.
	todayStr := time.Now().UTC().Format("2006-01-02")
	if t.todayStr != todayStr {
		t.today = UsageStats{
			StartedAt: time.Now().UTC(),
			ByTask:    make(map[string]int),
			ByModel:   make(map[string]int),
		}
		t.todayStr = todayStr
		// Clear daily cap pause on new day.
		if t.paused && t.maxCallsPerDay > 0 {
			t.paused = false
			t.pauseReason = ""
		}
	}

	t.addToStats(&t.session, m)
	t.addToStats(&t.today, m)
	t.addToStats(&t.lifetime, m)

	// Check caps. Count caps fire on exact-hit; the cost cap is
	// inherently approximate (depends on pricing data and post-call
	// reporting) so it fires on any exceedance.
	if t.maxCallsPerSession > 0 && t.session.Calls >= t.maxCallsPerSession {
		t.paused = true
		t.pauseReason = fmt.Sprintf("session LLM call cap reached (%d/%d)", t.session.Calls, t.maxCallsPerSession)
	}
	if t.maxCallsPerDay > 0 && t.today.Calls >= t.maxCallsPerDay {
		t.paused = true
		t.pauseReason = fmt.Sprintf("daily LLM call cap reached (%d/%d)", t.today.Calls, t.maxCallsPerDay)
	}
	if t.maxCostUSDPerDay > 0 && t.today.CostUSD >= t.maxCostUSDPerDay {
		t.paused = true
		t.pauseReason = fmt.Sprintf("daily LLM cost cap reached ($%.4f/$%.4f)", t.today.CostUSD, t.maxCostUSDPerDay)
	}
}

// IsPaused returns whether curation should pause due to caps.
func (t *UsageTracker) IsPaused() (bool, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.paused, t.pauseReason
}

// Unpause clears the paused state (for manual triggers).
func (t *UsageTracker) Unpause() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.paused = false
	t.pauseReason = ""
}

// Summary returns the full usage report.
func (t *UsageTracker) Summary() UsageSummary {
	t.mu.Lock()
	defer t.mu.Unlock()
	return UsageSummary{
		Session:  t.copyStats(t.session),
		Today:    t.copyStats(t.today),
		Lifetime: t.copyStats(t.lifetime),
	}
}

// TodayCalls returns the number of calls made today.
func (t *UsageTracker) TodayCalls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.today.Calls
}

// DailyCap returns the configured daily cap (0 = no cap).
func (t *UsageTracker) DailyCap() int {
	return t.maxCallsPerDay
}

// DailyCapPct returns usage as a percentage of the daily cap (0-100).
// Returns 0 if no cap is configured.
func (t *UsageTracker) DailyCapPct() int {
	if t.maxCallsPerDay <= 0 {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.today.Calls * 100 / t.maxCallsPerDay
}

// Persist writes usage data to disk.
func (t *UsageTracker) Persist() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.saveToDisk()
}

func (t *UsageTracker) addToStats(s *UsageStats, m CallMetrics) {
	s.Calls++
	if s.ByTask == nil {
		s.ByTask = make(map[string]int)
	}
	if s.ByModel == nil {
		s.ByModel = make(map[string]int)
	}
	s.ByTask[m.Task]++
	s.ByModel[m.Model]++
	s.InputTokens += m.InputTokens
	s.OutputTokens += m.OutputTokens
	s.CostUSD += m.CostUSD
	s.Credits += m.Credits
	s.LatencySumMs += m.LatencyMs
	if !m.Success {
		s.Errors++
	}
}

func (t *UsageTracker) copyStats(s UsageStats) UsageStats {
	c := s
	c.ByTask = make(map[string]int, len(s.ByTask))
	for k, v := range s.ByTask {
		c.ByTask[k] = v
	}
	c.ByModel = make(map[string]int, len(s.ByModel))
	for k, v := range s.ByModel {
		c.ByModel[k] = v
	}
	return c
}

// persistedUsage is the on-disk format.
type persistedUsage struct {
	Today    UsageStats `json:"today"`
	TodayStr string     `json:"today_date"` // "2006-01-02" for day reset
	Lifetime UsageStats `json:"lifetime"`
}

func (t *UsageTracker) usagePath() string {
	return filepath.Join(t.dataDir, "llm_usage.json")
}

func (t *UsageTracker) loadFromDisk() {
	if t.dataDir == "" {
		return
	}
	data, err := os.ReadFile(t.usagePath())
	if err != nil {
		return
	}
	var p persistedUsage
	if err := json.Unmarshal(data, &p); err != nil {
		return
	}

	// Reset today if it's a different day.
	todayStr := time.Now().UTC().Format("2006-01-02")
	if p.TodayStr == todayStr {
		t.today = p.Today
		if t.today.ByTask == nil {
			t.today.ByTask = make(map[string]int)
		}
		if t.today.ByModel == nil {
			t.today.ByModel = make(map[string]int)
		}
	}
	t.lifetime = p.Lifetime
	if t.lifetime.ByTask == nil {
		t.lifetime.ByTask = make(map[string]int)
	}
	if t.lifetime.ByModel == nil {
		t.lifetime.ByModel = make(map[string]int)
	}
}

func (t *UsageTracker) saveToDisk() {
	if t.dataDir == "" {
		return
	}
	// Persist t.todayStr verbatim, NOT a fresh time.Now() snapshot.
	// Previously: if a Record at 23:59:59 UTC ran then this save fired
	// at 00:00:01, the saved date label was "tomorrow" but t.today
	// still held yesterday's counters. On reload the loader checked
	// p.TodayStr == today() and treated the loaded counters as today's,
	// double-counting and skipping the daily cap on the day boundary.
	p := persistedUsage{
		Today:    t.today,
		TodayStr: t.todayStr,
		Lifetime: t.lifetime,
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(t.usagePath(), data, 0o600)
}
