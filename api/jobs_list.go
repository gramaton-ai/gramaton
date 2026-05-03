package api

import (
	"context"
	"fmt"
	"time"

	"github.com/gramaton-ai/gramaton/jobs"
)

// JobsListRequest filters and paginates the JobStore. Empty fields
// are unconstrained. Statuses[] is OR'd; other fields are AND'd.
type JobsListRequest struct {
	Status      string `json:"status,omitempty" jsonschema:"pending|running|completed|failed|cancelled (single status; omit for all)"`
	Kind        string `json:"kind,omitempty" jsonschema:"e.g. capture_batch (omit for all kinds)"`
	ClientToken string `json:"client_token,omitempty" jsonschema:"exact-match UUID"`
	Since       string `json:"since,omitempty" jsonschema:"RFC3339 lower-bound on created_at"`
	Until       string `json:"until,omitempty" jsonschema:"RFC3339 upper-bound on created_at"`
	Limit       int    `json:"limit,omitempty" jsonschema:"page size (1..200, default 50)"`
	Offset      int    `json:"offset,omitempty" jsonschema:"pagination offset (default 0)"`
}

// JobsListResponse is the lightweight summary projection. Heavy
// per-item fields (Result, ClientRefToID) are intentionally omitted
// here; callers that need them follow up with
// gramaton_capture_batch_status.
type JobsListResponse struct {
	Jobs   []JobSummary `json:"jobs"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

// JobSummary mirrors jobs.JobSummary at the api/ layer so callers
// don't have to import the internal jobs package types.
type JobSummary struct {
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	CompletedAt    time.Time `json:"completed_at,omitempty"`
	ClientToken    string    `json:"client_token,omitempty"`
	TotalItems     int       `json:"total_items"`
	ProcessedCount int       `json:"processed_count"`
	FailureReason  string    `json:"failure_reason,omitempty"`
}

// JobsListDescription is the MCP tool description for
// gramaton_jobs_list.
const JobsListDescription = `Enumerate persisted async jobs with optional filtering by status / kind / client_token / time range. Returns lightweight summaries (id, status, counts, timestamps) — Result payload and client_ref→id map are NOT included; for those call gramaton_capture_batch_status on a specific job.

Use this to find a recently-submitted job whose JobID was lost in transport, or to audit recent batch activity. Pagination via limit (max 200) and offset.`

// JobsList enumerates JobStore entries with the supplied filter.
func (a *API) JobsList(_ context.Context, req JobsListRequest) (JobsListResponse, *APIError) {
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

	filter := jobs.ListFilter{
		Status:      req.Status,
		Kind:        req.Kind,
		ClientToken: req.ClientToken,
		Limit:       limit,
		Offset:      req.Offset,
	}
	if req.Since != "" {
		t, err := time.Parse(time.RFC3339, req.Since)
		if err != nil {
			return JobsListResponse{}, ErrInvalid("since: " + err.Error())
		}
		filter.CreatedAfter = t
	}
	if req.Until != "" {
		t, err := time.Parse(time.RFC3339, req.Until)
		if err != nil {
			return JobsListResponse{}, ErrInvalid("until: " + err.Error())
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
			ClientToken:    r.ClientToken,
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
