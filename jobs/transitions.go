package jobs

// Job state machine. Allowed transitions are listed here as the
// single source of truth; Update and AdvanceStatus consult
// allowedTransition before writing.
//
//	pending --> running    (runner picks up)
//	pending --> cancelled  (cancel before runner picks up)
//	pending --> failed     (Phase 0 failure or panic before runner spawn)
//	running --> completed
//	running --> failed
//	running --> cancelled
//
// Terminal states (completed, failed, cancelled) have no outgoing
// transitions. An attempt to leave a terminal state returns
// ErrInvalidTransition.
//
// Status changes that don't actually transition (e.g., bumping
// ProcessedCount on a running job) take Update with j.Status ==
// current.Status; the whitelist check is bypassed in that case.

// allowedTransitions is the validated edge set of the state
// machine. Keyed by (from, to). Membership = allowed.
//
// Defined as a map literal for compactness; generated lazily on
// first call. The set is small (6 edges), so the lookup is O(1)
// hash with no realistic collision pressure.
var allowedTransitions = map[transitionKey]struct{}{
	{StatusPending, StatusRunning}:   {},
	{StatusPending, StatusCancelled}: {},
	{StatusPending, StatusFailed}:    {},
	{StatusRunning, StatusCompleted}: {},
	{StatusRunning, StatusFailed}:    {},
	{StatusRunning, StatusCancelled}: {},
}

type transitionKey struct {
	from string
	to   string
}

// allowedTransition reports whether (from -> to) is in the state
// machine's edge set. Same-status calls (from == to) are NOT
// validated here — Update / AdvanceStatus skip the whitelist
// check when statuses match.
func allowedTransition(from, to string) bool {
	_, ok := allowedTransitions[transitionKey{from, to}]
	return ok
}
