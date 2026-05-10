package api

import (
	"context"
	"fmt"

	"github.com/gramaton-ai/gramaton/curation"
	"github.com/gramaton-ai/gramaton/graph"
)

// Curation surfaces store-housekeeping operations: viewing the runner's
// current state, manually triggering a cycle, dry-running a cycle for
// review, and batch-classifying every pending record. Each verb is a
// separate method so transports get typed responses and per-method
// validation. The MCP tool gramaton_curation keeps its single
// `action` argument and dispatches across these methods.

// CurationStatusResponse reports the runner's current state plus the
// most-recent store manifest. Both fields may be nil when the runner
// has not yet completed its first cycle.
type CurationStatusResponse struct {
	Status   curation.EnhancedStatus `json:"status"`
	Manifest *curation.StoreManifest `json:"manifest,omitempty"`
}

// CurationTriggerResponse reports whether a manual trigger was
// accepted (false when a cycle was already in progress) and the
// runner's status after the call.
type CurationTriggerResponse struct {
	Triggered bool                    `json:"triggered"`
	Message   string                  `json:"message,omitempty"`
	Status    curation.EnhancedStatus `json:"status"`
}

// CurationDryRunResponse mirrors the trigger response but reports what
// the autonomous pipeline *would* have changed instead of applying the
// changes. Deterministic curation still ran (it is always safe).
type CurationDryRunResponse struct {
	DryRun         bool                     `json:"dry_run"`
	PlannedChanges []curation.PlannedChange `json:"planned_changes"`
	Classified     int                      `json:"classified"`
	Summaries      int                      `json:"summaries"`
	LLMCalls       int                      `json:"llm_calls"`
	Errors         int                      `json:"errors"`
	Status         curation.EnhancedStatus  `json:"status"`
}

// CurationBatchResponse wraps the BatchResult from the curation
// package so transport callers see the same shape regardless of
// surface.
type CurationBatchResponse struct {
	Result *curation.BatchResult `json:"result"`
}

// CurationDrainResponse reports the outcome of an artificial drain of
// the contradiction-detection candidate pool. See
// CurationDrainContradictions for the safety tradeoffs.
type CurationDrainResponse struct {
	Result *curation.DrainResult `json:"result"`
}

// Description constants are shared by HTTP, MCP, and CLI proxy
// transports so the surface text never drifts between them.
const (
	CurationStatusDescription  = "Get the current curation runner status and latest store manifest. Returns immediately. pending_count = records awaiting classification (work to do); concept_candidates = keywords above emergence threshold (telemetry signal, not a backlog)."
	CurationTriggerDescription = "Run a curation cycle now. Returns triggered=false (with the prior status) when a cycle is already in progress."
	CurationDryRunDescription  = "Preview what an autonomous curation cycle would do without applying changes. The deterministic phase still runs (it is always safe)."
	CurationBatchDescription   = "Classify every pending record in one call (LLM required). Use when piggyback curation has fallen behind."
	CurationDrainDescription   = "Artificially drain the contradiction-detection candidate pool by writing no_contradiction edges (marked artificial=true) on every in-window pair without an existing edge. No LLM calls. Use when the pool accumulated under pre-fix binaries and the operator does not want to pay the ambient Sonnet cost of organic drain. Tradeoff: real contradictions in the drained set will not be flagged. See design-decisions.md D38."
)

// CurationStatus returns the runner's status and current manifest.
// Cheap; safe to call frequently. Returns ErrUnavailable if the
// runner is not configured.
func (a *API) CurationStatus(ctx context.Context) (CurationStatusResponse, *APIError) {
	if a.runner == nil {
		return CurationStatusResponse{}, ErrUnavailable("curation is not enabled")
	}
	return CurationStatusResponse{
		Status:   a.runner.Status(),
		Manifest: a.runner.Manifest(),
	}, nil
}

// CurationTrigger asks the runner to start a cycle. Returns
// triggered=false (not an error) when a cycle is already in flight.
func (a *API) CurationTrigger(ctx context.Context) (CurationTriggerResponse, *APIError) {
	if a.runner == nil {
		return CurationTriggerResponse{}, ErrUnavailable("curation is not enabled")
	}
	if !a.runner.Trigger(ctx) {
		return CurationTriggerResponse{
			Triggered: false,
			Message:   "curation cycle already in progress",
			Status:    a.runner.Status(),
		}, nil
	}
	return CurationTriggerResponse{
		Triggered: true,
		Status:    a.runner.Status(),
	}, nil
}

// CurationDryRun runs the autonomous curation pipeline without
// applying changes. The LLM is still called so callers see what
// would have happened. Deterministic curation runs normally.
func (a *API) CurationDryRun(ctx context.Context) (CurationDryRunResponse, *APIError) {
	if a.runner == nil {
		return CurationDryRunResponse{}, ErrUnavailable("curation is not enabled")
	}
	result := a.runner.TriggerDryRun(ctx)
	return CurationDryRunResponse{
		DryRun:         true,
		PlannedChanges: result.PlannedChanges,
		Classified:     result.Classified,
		Summaries:      result.SummariesGenerated,
		LLMCalls:       result.LLMCalls,
		Errors:         result.Errors,
		Status:         a.runner.Status(),
	}, nil
}

// CurationDrainContradictions artificially marks every in-window
// contradiction-candidate pair as "no_contradiction" without calling
// the LLM. The operator is saying "I don't want to pay for the
// autonomous pass to organically drain this pool; I accept that real
// contradictions in the drained set won't be flagged." Edges carry
// an artificial: true property so future re-check logic can
// distinguish them from LLM-verified marks.
func (a *API) CurationDrainContradictions(ctx context.Context) (CurationDrainResponse, *APIError) {
	if a.engine == nil {
		return CurationDrainResponse{}, ErrInternal("engine not configured")
	}
	cfg := a.engine.Config()
	result, err := curation.DrainContradictionsNoLLM(ctx, a.engine, cfg, a.log)
	if err != nil {
		a.log.Error("contradiction drain failed", "component", "curation", "err", err)
		return CurationDrainResponse{Result: result}, ErrInternal("drain failed")
	}
	return CurationDrainResponse{Result: result}, nil
}

// CurationBatch classifies every pending record in one pass. Requires
// an LLM provider on the engine. Long-running; the caller should
// expect this to block for the duration.
func (a *API) CurationBatch(ctx context.Context) (CurationBatchResponse, *APIError) {
	if a.runner == nil {
		return CurationBatchResponse{}, ErrUnavailable("curation is not enabled")
	}
	if a.engine.LLM() == nil {
		return CurationBatchResponse{}, ErrUnavailable("LLM provider is required for batch curation")
	}
	cfg := a.engine.Config()
	result, err := curation.RunBatchClassification(ctx, a.engine, a.engine.LLM(), cfg, a.log)
	if err != nil {
		a.log.Error("batch curation failed", "component", "curation", "err", err)
		return CurationBatchResponse{}, ErrInternal("batch processing failed")
	}
	return CurationBatchResponse{Result: result}, nil
}

// stuckTaskPolicy describes how to identify and reset a stuck record
// for one curation task. Mirrors curation/task_retry.go's
// taskRetryPolicy in spirit (status + attempts + error properties),
// but adds the StatusResetValue field naming the value to flip back
// to on a reset (each task has its own "available for work" status:
// "captured" for classify, "pending" for synthesis).
type stuckTaskPolicy struct {
	Task             string // human-readable name reported to operators
	StatusKey        string // node property holding the lifecycle status
	StatusStuckValue string // value indicating the record is stuck
	StatusResetValue string // value to reset to on un-stick
	AttemptsKey      string // monotonic per-record retry counter
	ErrorKey         string // last failure reason (truncated)
}

// stuckTaskPolicies enumerates the curation tasks that have a
// stuck-after-N retry flow today. Adding a future task: register its
// policy here. The list is read at request time so additions are
// picked up without further wiring.
var stuckTaskPolicies = []stuckTaskPolicy{
	{
		Task:             "classify",
		StatusKey:        "processing_status",
		StatusStuckValue: "stuck",
		StatusResetValue: "captured",
		AttemptsKey:      "classify_attempts",
		ErrorKey:         "last_classify_error",
	},
	{
		Task:             "synthesis",
		StatusKey:        "synthesis_status",
		StatusStuckValue: "stuck",
		StatusResetValue: "pending",
		AttemptsKey:      "synthesis_attempts",
		ErrorKey:         "last_synthesis_error",
	},
}

// StuckRecord identifies a single record-task pair flagged as stuck.
// A record can in principle appear under multiple tasks if it has
// multiple per-task status fields stuck simultaneously, though in
// practice each task targets a different node shape.
type StuckRecord struct {
	ID    string `json:"id"`
	Task  string `json:"task"`
	Error string `json:"error,omitempty"`
}

// CurationListStuckResponse is the read-only stuck-record inventory.
// Records is the per-record breakdown; Counts is a per-task summary
// for callers that only need totals.
type CurationListStuckResponse struct {
	Records []StuckRecord  `json:"records"`
	Counts  map[string]int `json:"counts"`
}

// CurationResetStuckRequest selects which stuck records to un-stick.
// IDs empty = reset every stuck record. IDs non-empty = reset only
// those records (and only the ones in that set that are actually
// stuck; non-stuck IDs in the list are silently ignored).
type CurationResetStuckRequest struct {
	IDs []string `json:"ids,omitempty" jsonschema:"specific record IDs to reset; empty resets all stuck records"`
}

// CurationResetStuckResponse reports how many records were reset and
// the per-task breakdown. Reset is the total across all tasks.
type CurationResetStuckResponse struct {
	Reset  int            `json:"reset"`
	Counts map[string]int `json:"counts"`
}

// CurationListStuck walks the graph and returns every record with a
// stuck task status. Read-only; safe to call frequently.
func (a *API) CurationListStuck(ctx context.Context) (CurationListStuckResponse, *APIError) {
	_ = ctx
	a.engine.RLock()
	defer a.engine.RUnlock()

	var records []StuckRecord
	counts := map[string]int{}
	g := a.engine.Graph()
	it := g.NodeIterator()
	for it.Next() {
		n := it.Node()
		for _, p := range stuckTaskPolicies {
			status, ok := n.Properties.GetString(p.StatusKey)
			if !ok || status != p.StatusStuckValue {
				continue
			}
			lastErr, _ := n.Properties.GetString(p.ErrorKey)
			records = append(records, StuckRecord{
				ID:    n.ID,
				Task:  p.Task,
				Error: lastErr,
			})
			counts[p.Task]++
		}
	}
	it.Close()

	return CurationListStuckResponse{
		Records: records,
		Counts:  counts,
	}, nil
}

// CurationResetStuck flips stuck records back to their pre-failure
// status and clears their per-task attempts counter + last-error
// property. The next curation cycle will retry them. Operator-driven
// recovery; use the matching CLI verb gramaton curation
// stuck-records-reset which adds a count + LLM-cost-warning + Y/N
// confirmation around this call.
//
// When req.IDs is empty: reset every stuck record across all tasks.
// When req.IDs is non-empty: reset only the listed records; non-stuck
// IDs in the list are silently ignored (no error -- caller may pass
// a list from a prior CurationListStuck snapshot whose state has
// since changed).
func (a *API) CurationResetStuck(ctx context.Context, req CurationResetStuckRequest) (CurationResetStuckResponse, *APIError) {
	_ = ctx
	if len(req.IDs) > MaxResetStuckIDs {
		return CurationResetStuckResponse{}, ErrInvalid(fmt.Sprintf("ids: too many (%d > max %d)", len(req.IDs), MaxResetStuckIDs))
	}
	a.engine.Lock()
	defer a.engine.Unlock()

	var allowlist map[string]bool
	if len(req.IDs) > 0 {
		allowlist = make(map[string]bool, len(req.IDs))
		for _, id := range req.IDs {
			allowlist[id] = true
		}
	}

	// Phase 1: collect what to reset (do not mutate during iteration --
	// the existing curation passes follow this same two-phase shape).
	type pendingReset struct {
		id     string
		policy stuckTaskPolicy
	}
	var resets []pendingReset
	g := a.engine.Graph()
	it := g.NodeIterator()
	for it.Next() {
		n := it.Node()
		if allowlist != nil && !allowlist[n.ID] {
			continue
		}
		for _, p := range stuckTaskPolicies {
			status, ok := n.Properties.GetString(p.StatusKey)
			if ok && status == p.StatusStuckValue {
				resets = append(resets, pendingReset{id: n.ID, policy: p})
			}
		}
	}
	it.Close()

	// Phase 2: apply the resets and emit a CommitAction per record so
	// gramaton_log surfaces who was reset by this operation.
	counts := map[string]int{}
	var actions []graph.CommitAction
	for _, r := range resets {
		a.engine.SetProp(r.id, r.policy.StatusKey, graph.StringProperty(r.policy.StatusResetValue))
		a.engine.SetProp(r.id, r.policy.AttemptsKey, graph.Int64Property(0))
		_ = a.engine.Graph().RemoveNodeProperty(r.id, r.policy.ErrorKey)
		counts[r.policy.Task]++
		actions = append(actions, graph.CommitAction{
			Kind:     graph.ActionCurationStuckReset,
			RecordID: r.id,
		})
	}

	if len(resets) > 0 {
		if _, err := a.engine.Save("curation: reset stuck records", actions...); err != nil {
			a.log.Warn("save failed", "component", "curation", "op", "reset_stuck", "err", err)
			return CurationResetStuckResponse{}, ErrInternal("failed to save reset")
		}
	}

	return CurationResetStuckResponse{
		Reset:  len(resets),
		Counts: counts,
	}, nil
}
