package api

import (
	"context"

	"github.com/gramaton-ai/gramaton/curation"
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
	CurationStatusDescription  = "Get the current curation runner status and latest store manifest. Returns immediately."
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
