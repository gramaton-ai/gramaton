// Package jobs implements persistent tracking for long-running
// asynchronous operations. Each Job is a serialized state record
// stored in a dedicated bbolt file (jobs.db), separate from the
// engine's index store and from the graph's commit log.
//
// Why a separate package + file:
//
//   - Search isolation by construction: jobs are not graph nodes,
//     so the BM25 / vector / curation paths never see them. No
//     filtering machinery to maintain.
//   - Backup integration: jobs.db is included in store snapshots
//     via a bbolt-native View+WriteTo to avoid torn-page reads.
//     (Layer 2 wires this; this package owns the storage.)
//   - Generic from day one: Job.Kind distinguishes capture_batch,
//     f4_import, future async operations. The state machine,
//     persistence, GC, and lookup are all kind-agnostic.
//
// Concurrency: bbolt's MVCC semantics mean concurrent View calls
// return coherent snapshots even while Update transactions are in
// flight. Update + AdvanceStatus serialize via the bbolt write
// transaction (one writer at a time, multiple readers).
//
// Schema evolution policy: Job.FormatVersion is set to
// CurrentFormatVersion on Create. On Get, if a stored Job has a
// lower FormatVersion, the migration sequence runs in-memory and
// the result is written back via Update — so subsequent Gets do
// not re-trigger migration. Per-Get migration (rather than a
// startup walk) avoids half-migrated state if migration fails.
//
// Adding fields: additive only, with `omitempty` JSON tags. Renames
// require bumping CurrentFormatVersion and adding a migration
// function.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// CurrentFormatVersion is the on-disk Job schema version produced
// by this build. Older versions get migrated on Get (no-op at v1).
const CurrentFormatVersion = 1

// Bucket name. Single bucket; key=job_id, value=json(Job). Linear
// scan for client-token lookup; N is small.
const bucketName = "jobs"

// Sentinel errors. Callers compare with errors.Is.
var (
	ErrNotFound            = errors.New("jobs: not found")
	ErrInvalidTransition   = errors.New("jobs: invalid status transition")
	ErrFutureFormatVersion = errors.New("jobs: stored Job format version is newer than this build")
)

// Status values. The transition whitelist in transitions.go enforces
// which (from, to) pairs are valid; Update and AdvanceStatus reject
// the rest with ErrInvalidTransition.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Kind values. Each async-capable operation registers its kind here;
// JobStore filters and operator dashboards key off these strings.
const (
	KindCaptureBatch = "capture_batch"
)

// Job is the persisted record of an async operation.
//
// Field discipline: additive only. Renames require a new
// CurrentFormatVersion + a migration function. omitempty on
// optional fields keeps the on-disk encoding tight.
type Job struct {
	FormatVersion int    `json:"format_version"`
	ID            string `json:"id"`
	Kind          string `json:"kind"`   // "capture_batch" | "f4_import" | ...
	Status        string `json:"status"` // see Status* constants

	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`

	ClientToken     string `json:"client_token,omitempty"`
	RequestHash     string `json:"request_hash,omitempty"`
	SupersedesJobID string `json:"supersedes_job_id,omitempty"`

	// TenantID scopes the job to a caller. Empty in single-tenant
	// deployments; populated from request context when caller
	// identity wires in. JobStore queries that don't restrict by
	// TenantID will return rows from every tenant -- always pass the
	// caller's TenantID through ListFilter and check Job.TenantID
	// against the caller before returning a single-job projection.
	TenantID string `json:"tenant_id,omitempty"`

	TotalItems     int `json:"total_items"`
	ProcessedCount int `json:"processed_count"`

	// ClientRefToID accumulates ClientRef -> ULID assignments
	// across chunks. Persisted so cross-chunk edge resolution
	// survives restart. Nil until first chunk commits.
	ClientRefToID map[string]string `json:"client_ref_to_id,omitempty"`

	// Result is the serialized final response (CaptureBatchResponse
	// for capture_batch jobs). Populated when Status reaches a
	// terminal value.
	Result json.RawMessage `json:"result,omitempty"`

	// Errors grows as chunks fail; never replaced wholesale.
	Errors []ItemError `json:"errors,omitempty"`

	// FailureReason is populated when Status=="failed":
	// "server_restart" | "edge_fixup_failed" | "chunk_N_save_failed" |
	// "jobstore_update_failed" | "panicked" | etc.
	FailureReason string `json:"failure_reason,omitempty"`
}

// ItemError records a per-item failure inside a job. The Index
// refers to the original request's Items[] position; ClientRef
// echoes the caller's per-item label when one was supplied.
type ItemError struct {
	Index     int    `json:"index"`
	ClientRef string `json:"client_ref,omitempty"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

// JobSummary is the lightweight projection returned by List.
// Drops the heavy Result and ClientRefToID payloads — callers that
// need them call Get on a specific ID.
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

// ListFilter narrows the result set for List. Zero-value fields
// are unconstrained.
type ListFilter struct {
	Status        string    // "" = all
	Kind          string    // "" = all
	ClientToken   string    // "" = all
	TenantID      string    // "" matches "" (single-tenant); set to caller's tenant otherwise
	CreatedAfter  time.Time // inclusive lower bound; zero = no lower bound
	CreatedBefore time.Time // inclusive upper bound; zero = no upper bound
	Limit         int       // 0 = use MaxListLimit default
	Offset        int       // for pagination
}

// MaxListLimit caps the page size to keep response bounded.
const MaxListLimit = 200

// RetentionPolicy controls TTL-based GC. Zero duration means
// "keep forever" for that status.
type RetentionPolicy struct {
	Completed time.Duration
	Failed    time.Duration
	Cancelled time.Duration
}

// Store is the bbolt-backed Job store. Each Store owns its bbolt
// handle to a dedicated jobs.db file.
//
// All methods are safe for concurrent use; bbolt's MVCC is what
// serializes writes against itself.
type Store struct {
	db *bolt.DB

	// closeMu protects close() against double-close and against
	// post-close method calls. The bbolt db's own concurrency
	// covers in-flight transactions.
	closeMu sync.Mutex
	closed  bool
}

// SetNoSync disables bbolt's per-commit fsync for this store.
// Test-only; set by the engine under core.WithVolatileStorage.
func (s *Store) SetNoSync(v bool) { s.db.NoSync = v }

// New opens or creates jobs.db at the given path's directory.
// The bucket is initialized lazily on first write — but we ensure
// it exists on open so reads see a consistent shape.
func New(path string) (*Store, error) {
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return nil, fmt.Errorf("jobs: open %s: %w", path, err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketName))
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("jobs: init bucket: %w", err)
	}
	return &Store{db: db}, nil
}

// Close flushes and closes the bbolt handle. Safe to call multiple
// times.
func (s *Store) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}

// DB returns the underlying bbolt handle. Used by the backup walker
// to take a coherent snapshot via tx.WriteTo. NOT for general
// callers — use the typed methods.
func (s *Store) DB() *bolt.DB {
	return s.db
}

// Create writes a new Job. Sets FormatVersion to CurrentFormatVersion
// and CreatedAt to now if zero. Returns an error if a Job with the
// same ID already exists.
func (s *Store) Create(j *Job) error {
	if j == nil {
		return errors.New("jobs: nil job")
	}
	if j.ID == "" {
		return errors.New("jobs: empty id")
	}
	if j.FormatVersion == 0 {
		j.FormatVersion = CurrentFormatVersion
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = time.Now().UTC()
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			return errors.New("jobs: bucket missing")
		}
		key := []byte(j.ID)
		if existing := b.Get(key); existing != nil {
			return fmt.Errorf("jobs: id %q already exists", j.ID)
		}
		buf, err := json.Marshal(j)
		if err != nil {
			return fmt.Errorf("jobs: marshal: %w", err)
		}
		return b.Put(key, buf)
	})
}

// Get returns the Job with the given ID. Returns ErrNotFound if
// missing. Triggers per-Get migration if the stored FormatVersion
// is older; the migrated Job is written back so subsequent Gets
// don't re-trigger.
func (s *Store) Get(id string) (*Job, error) {
	var j *Job
	var needsMigrate bool
	if err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			return errors.New("jobs: bucket missing")
		}
		raw := b.Get([]byte(id))
		if raw == nil {
			return ErrNotFound
		}
		j = &Job{}
		if err := json.Unmarshal(raw, j); err != nil {
			return fmt.Errorf("jobs: unmarshal %q: %w", id, err)
		}
		if j.FormatVersion > CurrentFormatVersion {
			return fmt.Errorf("%w: stored=%d current=%d",
				ErrFutureFormatVersion, j.FormatVersion, CurrentFormatVersion)
		}
		if j.FormatVersion < CurrentFormatVersion {
			needsMigrate = true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if needsMigrate {
		if err := migrate(j); err != nil {
			return nil, fmt.Errorf("jobs: migrate %q: %w", id, err)
		}
		// Persist the migrated version so subsequent Gets are no-ops.
		// Note: this is a Put, not an Update transition, so it
		// bypasses the state-transition whitelist intentionally.
		if err := s.putRaw(j); err != nil {
			return nil, fmt.Errorf("jobs: persist migration: %w", err)
		}
	}
	return j, nil
}

// putRaw writes a Job without transition validation. Used by
// Create and by per-Get migration. NOT exposed publicly because
// it bypasses the state machine; use Update for changes.
func (s *Store) putRaw(j *Job) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			return errors.New("jobs: bucket missing")
		}
		buf, err := json.Marshal(j)
		if err != nil {
			return fmt.Errorf("jobs: marshal: %w", err)
		}
		return b.Put([]byte(j.ID), buf)
	})
}

// Update writes a Job, validating any status transition against
// the whitelist. If j.Status differs from the stored Status, the
// transition (current -> j.Status) must be allowed by
// allowedTransition; otherwise Update returns ErrInvalidTransition
// without writing. If statuses match, Update is an unconditional
// write (e.g., for ProcessedCount bumps).
//
// The validation runs inside a single bbolt write transaction, so
// there's no read-then-write race against concurrent Update calls.
func (s *Store) Update(j *Job) error {
	if j == nil {
		return errors.New("jobs: nil job")
	}
	if j.ID == "" {
		return errors.New("jobs: empty id")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			return errors.New("jobs: bucket missing")
		}
		key := []byte(j.ID)
		raw := b.Get(key)
		if raw == nil {
			return ErrNotFound
		}
		var current Job
		if err := json.Unmarshal(raw, &current); err != nil {
			return fmt.Errorf("jobs: unmarshal current %q: %w", j.ID, err)
		}
		if current.Status != j.Status {
			if !allowedTransition(current.Status, j.Status) {
				return fmt.Errorf("%w: %q -> %q",
					ErrInvalidTransition, current.Status, j.Status)
			}
		}
		buf, err := json.Marshal(j)
		if err != nil {
			return fmt.Errorf("jobs: marshal: %w", err)
		}
		return b.Put(key, buf)
	})
}

// AdvanceStatus is a CAS-style helper for the runner: atomically
// validate (current -> newStatus), apply mutator on the in-memory
// Job (caller mutates ProcessedCount, ClientRefToID, etc.), set
// status to newStatus, and write. All in one bbolt transaction.
//
// Returns ErrInvalidTransition if the transition isn't allowed —
// e.g., a runner trying to flip pending -> running but a concurrent
// cancel has already flipped pending -> cancelled. The runner is
// expected to exit cleanly on this error.
func (s *Store) AdvanceStatus(id, newStatus string, mutator func(*Job)) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			return errors.New("jobs: bucket missing")
		}
		key := []byte(id)
		raw := b.Get(key)
		if raw == nil {
			return ErrNotFound
		}
		var j Job
		if err := json.Unmarshal(raw, &j); err != nil {
			return fmt.Errorf("jobs: unmarshal %q: %w", id, err)
		}
		if j.Status != newStatus {
			if !allowedTransition(j.Status, newStatus) {
				return fmt.Errorf("%w: %q -> %q",
					ErrInvalidTransition, j.Status, newStatus)
			}
		}
		if mutator != nil {
			mutator(&j)
		}
		j.Status = newStatus
		buf, err := json.Marshal(&j)
		if err != nil {
			return fmt.Errorf("jobs: marshal: %w", err)
		}
		return b.Put(key, buf)
	})
}

// Delete removes the Job. Returns nil if the Job didn't exist
// (idempotent).
func (s *Store) Delete(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			return errors.New("jobs: bucket missing")
		}
		return b.Delete([]byte(id))
	})
}

// FindByClientToken returns the most recently-created Job with the
// given (token, tenant) pair. ClientToken collisions across tenants
// are independent: tenant A's token "abc" never matches tenant B's.
// Linear scan; N is bounded (jobs accumulate slowly). Returns
// (nil, nil) on no match.
func (s *Store) FindByClientToken(token, tenant string) (*Job, error) {
	if token == "" {
		return nil, nil
	}
	var found *Job
	if err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			return errors.New("jobs: bucket missing")
		}
		return b.ForEach(func(_, v []byte) error {
			var j Job
			if err := json.Unmarshal(v, &j); err != nil {
				return fmt.Errorf("jobs: unmarshal during scan: %w", err)
			}
			if j.ClientToken != token {
				return nil
			}
			if j.TenantID != tenant {
				return nil
			}
			if found == nil || j.CreatedAt.After(found.CreatedAt) {
				cp := j
				found = &cp
			}
			return nil
		})
	}); err != nil {
		return nil, err
	}
	return found, nil
}

// ListInFlight returns all Jobs with status in {pending, running}.
// Used by engine startup to flip in-flight jobs to failed/server_restart.
func (s *Store) ListInFlight() ([]*Job, error) {
	var out []*Job
	if err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			return errors.New("jobs: bucket missing")
		}
		return b.ForEach(func(_, v []byte) error {
			var j Job
			if err := json.Unmarshal(v, &j); err != nil {
				return fmt.Errorf("jobs: unmarshal during scan: %w", err)
			}
			if j.Status == StatusPending || j.Status == StatusRunning {
				cp := j
				out = append(out, &cp)
			}
			return nil
		})
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// List returns JobSummary records matching the filter, paginated.
// Cheaper than fetching full Jobs because it omits the Result
// payload and the ClientRefToID map.
func (s *Store) List(f ListFilter) ([]*JobSummary, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = MaxListLimit
	}
	if limit > MaxListLimit {
		return nil, fmt.Errorf("jobs: limit %d exceeds MaxListLimit %d",
			limit, MaxListLimit)
	}
	skip := f.Offset
	out := make([]*JobSummary, 0)

	if err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			return errors.New("jobs: bucket missing")
		}
		return b.ForEach(func(_, v []byte) error {
			if len(out) >= limit {
				return nil
			}
			var j Job
			if err := json.Unmarshal(v, &j); err != nil {
				return fmt.Errorf("jobs: unmarshal during list: %w", err)
			}
			if !filterMatches(&j, f) {
				return nil
			}
			if skip > 0 {
				skip--
				return nil
			}
			out = append(out, summarize(&j))
			return nil
		})
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// RunGC walks the bucket once and deletes terminal jobs older than
// retention. Zero retention duration means "keep forever" for that
// status. In-flight (pending/running) jobs are never GC'd.
//
// Honors ctx cancellation: between every walked entry the loop
// checks ctx.Done(); if cancelled, returns the partial count and
// ctx.Err(). The bbolt View tx is held across the walk and released
// on return; the Update tx (delete pass) runs only if walk finished
// cleanly. Callers (the engine sweeper) cancel via ctx during
// shutdown so Close doesn't block on a long walk.
//
// Returns the number of jobs deleted.
func (s *Store) RunGC(ctx context.Context, now time.Time, ret RetentionPolicy) (int, error) {
	var toDelete []string

	if err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			return errors.New("jobs: bucket missing")
		}
		return b.ForEach(func(k, v []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			var j Job
			if err := json.Unmarshal(v, &j); err != nil {
				// Corrupted entry: skip rather than fail GC. The
				// repaired/removed handling is a separate concern.
				return nil
			}
			ttl := retentionFor(j.Status, ret)
			if ttl == 0 {
				return nil
			}
			if j.CompletedAt.IsZero() {
				return nil
			}
			if now.Sub(j.CompletedAt) >= ttl {
				toDelete = append(toDelete, string(k))
			}
			return nil
		})
	}); err != nil {
		return 0, err
	}

	if len(toDelete) == 0 {
		return 0, nil
	}

	if err := ctx.Err(); err != nil {
		return 0, err
	}

	if err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			return errors.New("jobs: bucket missing")
		}
		for _, id := range toDelete {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := b.Delete([]byte(id)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return len(toDelete), nil
}

// migrate runs the in-memory migration for j. v0 (legacy data
// without FormatVersion) becomes v1 by setting the field; no
// other changes. Future versions will chain migrations here
// (v1->v2, v2->v3, ...).
func migrate(j *Job) error {
	if j.FormatVersion == 0 {
		j.FormatVersion = 1
	}
	// v1 is current; no further migration steps yet.
	return nil
}

// summarize projects a Job into a JobSummary for List output.
func summarize(j *Job) *JobSummary {
	return &JobSummary{
		ID:             j.ID,
		Kind:           j.Kind,
		Status:         j.Status,
		CreatedAt:      j.CreatedAt,
		StartedAt:      j.StartedAt,
		CompletedAt:    j.CompletedAt,
		ClientToken:    j.ClientToken,
		TotalItems:     j.TotalItems,
		ProcessedCount: j.ProcessedCount,
		FailureReason:  j.FailureReason,
	}
}

// filterMatches returns true if j passes every constraint in f.
// Zero-value fields in f are unconstrained EXCEPT TenantID, which
// always requires equality (empty matches empty in single-tenant
// deployments). Time bounds are inclusive: CreatedAt == bound passes.
func filterMatches(j *Job, f ListFilter) bool {
	if f.Status != "" && j.Status != f.Status {
		return false
	}
	if f.Kind != "" && j.Kind != f.Kind {
		return false
	}
	if f.ClientToken != "" && j.ClientToken != f.ClientToken {
		return false
	}
	if j.TenantID != f.TenantID {
		return false
	}
	if !f.CreatedAfter.IsZero() && j.CreatedAt.Before(f.CreatedAfter) {
		return false
	}
	if !f.CreatedBefore.IsZero() && j.CreatedAt.After(f.CreatedBefore) {
		return false
	}
	return true
}

// retentionFor returns the configured retention duration for the
// given status, or 0 if not GC-eligible (in-flight or unknown).
func retentionFor(status string, ret RetentionPolicy) time.Duration {
	switch status {
	case StatusCompleted:
		return ret.Completed
	case StatusFailed:
		return ret.Failed
	case StatusCancelled:
		return ret.Cancelled
	default:
		return 0
	}
}
