package api

import "net/http"

// APIError is the canonical error type returned by every api method.
// Preserving the structured fields (vs. plain error wrapping) lets
// transports format the same error consistently:
//   - HTTP writes Code/Message/Retryable into the error envelope at HTTPStatus
//   - MCP emits the message with a structured error return
//   - CLI surfaces Code/Message
//
// Methods return (Response, *APIError); nil means success. Do not
// return a non-nil APIError alongside a partial Response -- callers
// should assume a non-nil APIError means the operation did not commit.
type APIError struct {
	Code       string // stable, machine-readable (e.g. "not_found", "input_error")
	Message    string // human-readable; safe to show end-users
	HTTPStatus int    // maps directly to HTTP response status
	Retryable  bool   // true when the caller can retry without changing the request
	Cause      error  // optional underlying error for errors.Is/As chaining (never serialized)
}

// Error makes APIError satisfy the error interface for callers that
// want to treat it as one. Transports typically do not use this; they
// read the fields directly.
func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

// Unwrap lets callers use errors.Is / errors.As to reach the underlying
// cause (e.g. graph.ErrNotFound) without string-matching on Code. Only
// meaningful when the constructor populates Cause -- most internal
// callsites don't, and transports never serialize it.
func (e *APIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Helpers. Every api method uses these so error codes are consistent
// across operations. If you find yourself writing a new error type,
// consider whether an existing one fits first.

// ErrMissing signals a required field is empty. 400, retryable once
// the caller includes the field.
func ErrMissing(msg string) *APIError {
	return &APIError{Code: "missing_field", Message: msg, HTTPStatus: http.StatusBadRequest, Retryable: true}
}

// ErrInvalid signals a field has the wrong shape/value. 400, retryable
// once the caller fixes the value.
func ErrInvalid(msg string) *APIError {
	return &APIError{Code: "input_error", Message: msg, HTTPStatus: http.StatusBadRequest, Retryable: true}
}

// ErrNotFound signals the target resource does not exist. 404.
// Not retryable -- the caller should not keep asking.
func ErrNotFound(msg string) *APIError {
	return &APIError{Code: "not_found", Message: msg, HTTPStatus: http.StatusNotFound, Retryable: false}
}

// ErrConflict signals a precondition that prevents the write
// (e.g. duplicate detected, record in wrong state). 409.
// Not retryable without a behavior change from the caller.
func ErrConflict(msg string) *APIError {
	return &APIError{Code: "conflict", Message: msg, HTTPStatus: http.StatusConflict, Retryable: false}
}

// ErrInternal signals an unexpected failure that the caller cannot fix.
// 500, retryable (the transient cases are the common ones).
func ErrInternal(msg string) *APIError {
	return &APIError{Code: "internal_error", Message: msg, HTTPStatus: http.StatusInternalServerError, Retryable: true}
}

// ErrForbidden signals an operation not permitted for this caller/surface
// (e.g. loopback-only endpoint called from a non-loopback origin). 403.
func ErrForbidden(msg string) *APIError {
	return &APIError{Code: "forbidden", Message: msg, HTTPStatus: http.StatusForbidden, Retryable: false}
}

// ErrPrepareRequired signals that gramaton_session_commit was called
// without a prior gramaton_session_prepare. 409 because the server is
// in a wrong-state rather than the request being malformed; retryable
// after the caller prepares.
func ErrPrepareRequired(msg string) *APIError {
	return &APIError{Code: "prepare_required", Message: msg, HTTPStatus: http.StatusConflict, Retryable: true}
}

// ErrUnavailable signals that an operation cannot proceed because a
// required dependency is not configured (e.g. curation runner is off,
// LLM provider is missing, no embedder). 503 because the server is
// healthy but the feature is not. Not retryable: the caller cannot
// fix this without an operator changing config.
func ErrUnavailable(msg string) *APIError {
	return &APIError{Code: "unavailable", Message: msg, HTTPStatus: http.StatusServiceUnavailable, Retryable: false}
}
