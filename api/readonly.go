package api

// rejectIfReadOnly returns ErrForbidden when the engine is in
// store-level read-only mode (frozen via the STORE manifest or forced
// via core.WithReadOnly). Every mutating API method calls this as its
// FIRST statement -- before validation, lock acquisition, and any
// in-memory graph mutation -- so a frozen store rejects logical
// writes uniformly with code "forbidden" (HTTP 403) and never builds
// partial in-memory state that the engine-level backstop could not
// undo.
//
// The engine keeps its own backstops (core.ErrStoreReadOnly from
// Save/WithWriteBatch) for anything that slips past the api surface;
// this guard is the canonical rejection point, and it names the
// operation so agents seeing the error know what was refused without
// correlating server logs.
//
// TestReadOnlyGuardCoversEveryAPIMethod enumerates every exported
// method on API and pins its WRITE/READ classification; a new
// mutating method that skips this guard trips that test.
func (a *API) rejectIfReadOnly(op string) *APIError {
	if !a.engine.ReadOnly() {
		return nil
	}
	return ErrForbidden("store is read-only: " + op + " is not permitted")
}
