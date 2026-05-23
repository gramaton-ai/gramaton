package api

import (
	"context"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/jobs"
)

// JobsListRequest filters and paginates the JobStore. Empty fields
// are unconstrained except TenantID, which is always read from
// context and never accepted from the wire (a tenant cannot list
// another tenant's jobs by passing their id).
type JobsListRequest struct {
	Status      string `json:"status,omitempty" jsonschema:"pending|running|completed|failed|cancelled (single status; omit for all)"`
	Kind        string `json:"kind,omitempty" jsonschema:"e.g. capture_batch (max 64 chars; omit for all kinds)"`
	ClientToken string `json:"client_token,omitempty" jsonschema:"exact-match UUID; scoped to the caller's tenant"`
	Since       string `json:"since,omitempty" jsonschema:"RFC3339 inclusive lower-bound on created_at; max 64 chars"`
	Until       string `json:"until,omitempty" jsonschema:"RFC3339 inclusive upper-bound on created_at; max 64 chars"`
	Limit       int    `json:"limit,omitempty" jsonschema:"page size (1..200, default 50)"`
	Offset      int    `json:"offset,omitempty" jsonschema:"pagination offset (0..100000)"`
}

// JobsListResponse is the lightweight summary projection. Heavy
// per-item fields (Result, ClientRefToID) are intentionally omitted
// here; callers that need them follow up with
// gramaton_save_batch_status.
type JobsListResponse struct {
	Jobs   []JobSummary `json:"jobs"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

// JobSummary mirrors jobs.JobSummary at the api/ layer so callers
// don't have to import the internal jobs package types.
//
// ClientToken is intentionally NOT included here. Listing other
// tenants' tokens (in the multi-tenant future) would let one caller
// guess another's idempotency window. A caller that wants to look up
// their own jobs by token uses SaveBatchStatus on a known JobID,
// or filters JobsList by their own ClientToken via the request.
type JobSummary struct {
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	CompletedAt    time.Time `json:"completed_at,omitempty"`
	TotalItems     int       `json:"total_items"`
	ProcessedCount int       `json:"processed_count"`
	FailureReason  string    `json:"failure_reason,omitempty"`
}

// JobsListDescription is the MCP tool description for
// gramaton_jobs_list.
const JobsListDescription = `Enumerate persisted async jobs with optional filtering by status / kind / client_token / time range. Returns lightweight summaries (id, status, counts, timestamps) — Result payload and client_ref→id map are NOT included; for those call gramaton_save_batch_status on a specific job.

Use this to find a recently-submitted job whose JobID was lost in transport, or to audit recent batch activity. Pagination via limit (max 200) and offset.`

// JobsList enumerates JobStore entries with the supplied filter,
// scoped to the caller's tenant. Cross-tenant rows never leak: the
// JobStore filter pins TenantID to the value derived from context.
func (a *API) JobsList(ctx context.Context, req JobsListRequest) (JobsListResponse, *APIError) {
	store := a.engine.JobStore()
	if store == nil {
		return JobsListResponse{}, ErrUnavailable("jobstore unavailable")
	}

	if req.Status != "" {
		switch req.Status {
		case jobs.StatusPending, jobs.StatusRunning, jobs.StatusCompleted, jobs.StatusFailed, jobs.StatusCancelled:
		default:
			return JobsListResponse{}, ErrInvalid(fmt.Sprintf("unknown status %q", req.Status))
		}
	}
	if len(req.Kind) > MaxKindLen {
		return JobsListResponse{}, ErrInvalid(fmt.Sprintf("kind exceeds %d characters", MaxKindLen))
	}
	if req.ClientToken != "" {
		if err := validateClientToken(req.ClientToken); err != nil {
			return JobsListResponse{}, ErrInvalid(err.Error())
		}
	}
	if len(req.Since) > MaxRFC3339Len {
		return JobsListResponse{}, ErrInvalid(fmt.Sprintf("since exceeds %d characters", MaxRFC3339Len))
	}
	if len(req.Until) > MaxRFC3339Len {
		return JobsListResponse{}, ErrInvalid(fmt.Sprintf("until exceeds %d characters", MaxRFC3339Len))
	}

	limit := req.Limit
	if limit <= 0 {
		limit = DefaultJobsListLimit
	}
	if limit > MaxJobsListLimit {
		return JobsListResponse{}, ErrInvalid(fmt.Sprintf("limit exceeds %d", MaxJobsListLimit))
	}
	if req.Offset < 0 {
		return JobsListResponse{}, ErrInvalid("offset must not be negative")
	}
	if req.Offset > MaxJobsListOffset {
		return JobsListResponse{}, ErrInvalid(fmt.Sprintf("offset exceeds %d", MaxJobsListOffset))
	}

	filter := jobs.ListFilter{
		Status:      req.Status,
		Kind:        req.Kind,
		ClientToken: req.ClientToken,
		TenantID:    tenantFromContext(ctx),
		Limit:       limit,
		Offset:      req.Offset,
	}
	if req.Since != "" {
		t, err := time.Parse(time.RFC3339, req.Since)
		if err != nil {
			return JobsListResponse{}, ErrInvalid("since must be RFC3339 (e.g. 2026-01-02T15:04:05Z)")
		}
		filter.CreatedAfter = t
	}
	if req.Until != "" {
		t, err := time.Parse(time.RFC3339, req.Until)
		if err != nil {
			return JobsListResponse{}, ErrInvalid("until must be RFC3339 (e.g. 2026-01-02T15:04:05Z)")
		}
		filter.CreatedBefore = t
	}
	if !filter.CreatedAfter.IsZero() && !filter.CreatedBefore.IsZero() && filter.CreatedAfter.After(filter.CreatedBefore) {
		return JobsListResponse{}, ErrInvalid("since must not be after until")
	}

	rows, err := store.List(filter)
	if err != nil {
		a.log.Warn("jobs_list: list failed", "err", err)
		return JobsListResponse{}, ErrInternal("failed to list jobs")
	}
	out := make([]JobSummary, len(rows))
	for i, r := range rows {
		out[i] = JobSummary{
			ID:             r.ID,
			Kind:           r.Kind,
			Status:         r.Status,
			CreatedAt:      r.CreatedAt,
			StartedAt:      r.StartedAt,
			CompletedAt:    r.CompletedAt,
			TotalItems:     r.TotalItems,
			ProcessedCount: r.ProcessedCount,
			FailureReason:  r.FailureReason,
		}
	}
	return JobsListResponse{
		Jobs:   out,
		Total:  len(out),
		Limit:  limit,
		Offset: req.Offset,
	}, nil
}
