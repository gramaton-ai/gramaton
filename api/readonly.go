package api

// rejectIfReadOnly returns ErrForbidden when the engine is in
// store-level read-only mode. In production this flag comes from the
// STORE manifest (set by `gramaton store freeze`, read at engine open
// -- see core/engine.go's openFiles); core.WithReadOnly only forces it
// in tests that attach to a store they must not mutate. Every
// mutating API method calls this as its FIRST statement -- before
// validation, lock acquisition, and any in-memory graph mutation --
// so a frozen store rejects logical writes uniformly with code
// "forbidden" (HTTP 403).
//
// WHY a guard AND an engine backstop: the guard is the UX layer. It
// is the canonical rejection point and names the refused operation,
// so agents seeing the error know what was refused without
// correlating server logs. Enforcement is the engine backstop --
// Save and WithWriteBatch re-check ReadOnly and reject with
// core.ErrStoreReadOnly.
//
// The split matters because the guard runs before the engine lock,
// so it is check-then-act: the one in-process flip (BackupRestore of
// a frozen archive, which holds the write lock across the swap; CLI
// freeze/thaw refuse while a server is alive) can happen AFTER a
// writer passed its guard and BEFORE it acquired the lock. That
// straddling writer resumes against the now-frozen store and loses
// at the backstop: no durable graph write lands. It may leave
// transient in-memory drift (e.g. a node inserted before Save
// rejected), which the next reload of the frozen artifact clears.
// Accepted by design for a single-process alpha: guard = UX, Save
// backstop = enforcement, and on a mid-request flip the backstop
// wins.
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
