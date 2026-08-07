package api

// Typed response shapes for the collections cluster + SessionSave.
// Each method's response was map[string]any before the canonical-api
// refactor; this file replaces those with named structs whose JSON
// shapes match the prior wire output byte-for-byte.
//
// Conventions:
//   - omitempty on every optional field so present/absent on the wire
//     mirrors the old map[string]any behavior.
//   - nested optional sub-objects use pointer types so nil = absent.
//   - per-item dynamic shapes (CollectionItem.Fields,
//     SaveSegment-derived response data) stay map[string]any
//     because the sub-shape is collection-schema-driven; typing per
//     schema is out of scope.

// CollectionCreateResponse: {id, name}.
type CollectionCreateResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CollectionListEntry is one row in CollectionListResponse.Collections.
// Description / HasSchema / Retired / CreatedAt are optional and are
// emitted only when present on the underlying node.
type CollectionListEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	HasSchema   bool   `json:"has_schema,omitempty"`
	ItemCount   int    `json:"item_count"`
	Retired     bool   `json:"retired,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// CollectionListResponse pagination matches the original map shape:
// HasMore + NextOffset are emitted only when a next page exists.
type CollectionListResponse struct {
	Collections []CollectionListEntry `json:"collections"`
	Showing     int                   `json:"showing"`
	Total       int                   `json:"total"`
	HasMore     bool                  `json:"has_more,omitempty"`
	NextOffset  int                   `json:"next_offset,omitempty"`
}

// CollectionItem is one row in CollectionItemsResponse.Items. Fields
// is dynamic (collection-schema-driven, optionally projected); typing
// it as map[string]any preserves wire shape and avoids per-schema
// codegen.
type CollectionItem struct {
	ID             string         `json:"id"`
	CreatedAt      string         `json:"created_at,omitempty"`
	Fields         map[string]any `json:"fields,omitempty"`
	NeedsMigration []string       `json:"needs_migration,omitempty"`
}

// CollectionMigrationState describes an in-progress schema migration.
// Returned inside CollectionItemsResponse, CollectionSchemaReadResponse,
// and CollectionSchemaUpdateResponse. Done/Remaining are only
// populated by CollectionItems (computed over the live member set);
// Message is only set by CollectionSchemaUpdate.
type CollectionMigrationState struct {
	Fields    []string `json:"fields"`
	Total     int64    `json:"total"`
	Done      int      `json:"done,omitempty"`
	Remaining int      `json:"remaining,omitempty"`
	Message   string   `json:"message,omitempty"`
}

// CollectionItemsResponse covers both the live (HEAD) read and the
// point-in-time (as_of) branch. AsOf + Semantics are emitted only on
// the point-in-time branch.
type CollectionItemsResponse struct {
	CollectionID string                    `json:"collection_id"`
	Items        []CollectionItem          `json:"items"`
	Count        int                       `json:"count"`
	Migration    *CollectionMigrationState `json:"migration,omitempty"`
	AsOf         string                    `json:"as_of,omitempty"`
	Semantics    string                    `json:"semantics,omitempty"`
}

// CollectionAddResponse covers both the normal-add and the
// minimal-curation idempotent-dedup branches. Deduplicated is true
// when the item's title already existed on a curation=minimal
// collection; the ID points at the pre-existing item (no new node
// created). CollectionName / CollectionDescription echo the target
// collection's identity so the filing agent sees what the
// collection is for at the moment of adding (#98); both are
// omitempty so the wire shape stays backward compatible.
type CollectionAddResponse struct {
	ID                    string `json:"id"`
	CollectionID          string `json:"collection_id"`
	CollectionName        string `json:"collection_name,omitempty"`
	CollectionDescription string `json:"collection_description,omitempty"`
	Deduplicated          bool   `json:"deduplicated,omitempty"`
}

// CollectionRemoveResponse: {removed, item_id, collection_id}.
type CollectionRemoveResponse struct {
	Removed      bool   `json:"removed"`
	ItemID       string `json:"item_id"`
	CollectionID string `json:"collection_id"`
}

// CollectionUpdateResponse: {updated, item_id}.
type CollectionUpdateResponse struct {
	Updated bool   `json:"updated"`
	ItemID  string `json:"item_id"`
}

// CollectionMoveResponse: {moved, item_id, from/to_collection_id}.
type CollectionMoveResponse struct {
	Moved            bool   `json:"moved"`
	ItemID           string `json:"item_id"`
	FromCollectionID string `json:"from_collection_id"`
	ToCollectionID   string `json:"to_collection_id"`
}

// CollectionRenameResponse: {renamed, id, name}.
type CollectionRenameResponse struct {
	Renamed bool   `json:"renamed"`
	ID      string `json:"id"`
	Name    string `json:"name"`
}

// CollectionDeleteResponse covers both the retire and unretire
// branches. Exactly one of Retired / Unretired is true. ItemsPreserved
// is set only on the retire branch (count of items left intact under
// the retired collection).
type CollectionDeleteResponse struct {
	ID             string `json:"id"`
	Retired        bool   `json:"retired,omitempty"`
	Unretired      bool   `json:"unretired,omitempty"`
	ItemsPreserved int    `json:"items_preserved,omitempty"`
}

// CollectionSchemaReadResponse: {collection_id, schema?, migration?}.
type CollectionSchemaReadResponse struct {
	CollectionID string                    `json:"collection_id"`
	Schema       *CollectionSchema         `json:"schema,omitempty"`
	Migration    *CollectionMigrationState `json:"migration,omitempty"`
}

// CollectionSchemaUpdateResponse: {updated, collection_id, migration?}.
type CollectionSchemaUpdateResponse struct {
	Updated      bool                      `json:"updated"`
	CollectionID string                    `json:"collection_id"`
	Migration    *CollectionMigrationState `json:"migration,omitempty"`
}

// CollectionMigrateResponse: {migrated, field, migration_complete, failed?}.
//
// Failed is the per-item failure list (P3-B). Today the migration
// loop has no per-item failure path -- the only loop branch is "node
// gone between collect and apply" which silently `continue`s -- so
// Failed is omitempty and currently nil-emitted. The slot is
// reserved for future partial-success work where caller-recoverable
// failures (validation drift, contended writes) are recorded
// alongside the items that did migrate.
type CollectionMigrateResponse struct {
	Migrated          int           `json:"migrated"`
	Field             string        `json:"field"`
	MigrationComplete bool          `json:"migration_complete"`
	Failed            []ItemFailure `json:"failed,omitempty"`
}

// ItemFailure is the per-item failure shape used by SessionSave and
// CollectionMigrate (and structurally compatible with BatchAddFailure
// in the AddBatch path). Index correlates to the caller's input
// position; ItemID correlates to a pre-existing record the operation
// iterated over.
type ItemFailure struct {
	Index   int    `json:"index"`
	ItemID  string `json:"item_id,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SessionSaveResponse pins SessionSave's previous map[string]any
// shape. The legacy map emits session_only_segments unconditionally,
// so it has no omitempty here. Held is omitempty (emitted only when
// the save guard held at least one segment's Memory promotion).
// Failed is the per-segment failure list reserved for future
// partial-success work (see CollectionMigrateResponse.Failed for the
// same disclaimer).
type SessionSaveResponse struct {
	SessionID            string                 `json:"session_id"`
	SegmentsAdded        int                    `json:"segments_added"`
	SessionOnlySegments  int                    `json:"session_only_segments"`
	TopicsCreated        int                    `json:"topics_created"`
	MemoryRecordsCreated int                    `json:"memory_records_created"`
	EdgesCreated         int                    `json:"edges_created"`
	Boundary             *SaveBoundary          `json:"boundary,omitempty"`
	Held                 []SessionHeldPromotion `json:"held,omitempty"`
	Failed               []ItemFailure          `json:"failed,omitempty"`
}

// SessionHeldPromotion describes a segment whose Memory promotion the
// save guard held: the segment itself WAS created (the Sessions tier
// is append-only and always lands), but no Memory record exists for
// it yet. The hold persists in session state and is re-presented at
// the next session_prepare until resolved via
// gramaton_session_resolve_held: either update the similar existing
// record with this segment's knowledge (the server then wires the
// segment's provenance to it), or re-promote with allow_similar.
type SessionHeldPromotion struct {
	SegmentID string       `json:"segment_id"`
	Topic     string       `json:"topic"`
	Held      *HeldSimilar `json:"held"`
}

// SaveBoundary is the watermark emitted by a successful session_save.
// The bracketed Marker string is the LLM-friendly scoping primitive:
// the agent substring-scans its own conversation history for
// "[gramaton-save-boundary" to find the position of its most recent
// successful save and scope subsequent extraction to content that
// appeared after it. Timestamp and SessionID are present for hook
// consumers that want structured fields rather than parsing the
// marker string.
type SaveBoundary struct {
	Marker    string `json:"marker"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"session_id"`
}
