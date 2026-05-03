package api

import (
	"context"

	"github.com/gramaton-ai/gramaton/jobs"
)

// CaptureBatchStatusRequest selects a job by ID for a status read.
type CaptureBatchStatusRequest struct {
	JobID string `json:"job_id" jsonschema:"the job_id returned by gramaton_capture_batch"`
}

// CaptureBatchStatusResponse is the live snapshot of a Job. Errors[]
// surfaces per-item failures collected so far so a caller can correct
// inputs and retry without waiting for the run to finish.
// ClientRefToID maps caller-supplied refs to assigned ULIDs; with
// failed runs it lets a caller resume edge creation via gramaton_link.
type CaptureBatchStatusResponse struct {
	JobID          string             `json:"job_id"`
	Status         string             `json:"status"`
	Kind           string             `json:"kind"`
	TotalItems     int                `json:"total_items"`
	ProcessedCount int                `json:"processed_count"`
	FailureReason  string             `json:"failure_reason,omitempty"`
	Errors         []BatchItemFailure `json:"errors,omitempty"`
	ClientRefToID  map[string]string  `json:"client_ref_to_id,omitempty"`
	ClientToken    string             `json:"client_token,omitempty"`
}

// CaptureBatchStatusDescription is the MCP tool description for
// gramaton_capture_batch_status.
const CaptureBatchStatusDescription = `Inspect the live state of an async gramaton_capture_batch job. Returns status (pending|running|completed|failed|cancelled), total/processed counts, per-item errors collected so far, and the client_ref→id map so edge wiring can resume from a partial run.

Safe to poll repeatedly; the call is read-only against the JobStore.`

// CaptureBatchStatus reads the current Job state. Read-only; never
// touches the engine write lock. Cross-tenant access surfaces
// ErrNotFound rather than ErrForbidden so existence isn't leaked.
func (a *API) CaptureBatchStatus(ctx context.Context, req CaptureBatchStatusRequest) (CaptureBatchStatusResponse, *APIError) {
	if req.JobID == "" {
		return CaptureBatchStatusResponse{}, ErrMissing("job_id is required")
	}
	store := a.engine.JobStore()
	if store == nil {
		return CaptureBatchStatusResponse{}, ErrUnavailable("jobstore unavailable")
	}
	j, err := store.Get(req.JobID)
	if err != nil {
		if err == jobs.ErrNotFound {
			return CaptureBatchStatusResponse{}, ErrNotFound("job not found")
		}
		a.log.Warn("capture_batch_status: get failed", "job_id", req.JobID, "err", err)
		return CaptureBatchStatusResponse{}, ErrInternal("failed to read job")
	}
	if !tenantOwnsJob(tenantFromContext(ctx), j.TenantID) {
		return CaptureBatchStatusResponse{}, ErrNotFound("job not found")
	}
	return CaptureBatchStatusResponse{
		JobID:          j.ID,
		Status:         j.Status,
		Kind:           j.Kind,
		TotalItems:     j.TotalItems,
		ProcessedCount: j.ProcessedCount,
		FailureReason:  j.FailureReason,
		Errors:         failuresFromErrors(j.Errors),
		ClientRefToID:  copyRefMap(j.ClientRefToID),
		ClientToken:    j.ClientToken,
	}, nil
}

// failuresFromErrors is the inverse of errorsFromFailures: projects
// jobs.ItemError back into the api-layer BatchItemFailure shape so
// the response wire matches what the sync path returns.
func failuresFromErrors(in []jobs.ItemError) []BatchItemFailure {
	if len(in) == 0 {
		return nil
	}
	out := make([]BatchItemFailure, len(in))
	for i, e := range in {
		out[i] = BatchItemFailure{
			Index:     e.Index,
			ClientRef: e.ClientRef,
			Code:      e.Code,
			Message:   e.Message,
		}
	}
	return out
}

// copyRefMap returns a defensive copy so callers can't mutate the
// JobStore's in-memory state via the response.
func copyRefMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
