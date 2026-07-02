package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/graph"
)

// ResolveRequest marks a record as resolved (completed, superseded,
// abandoned, or obsolete) with an optional note.
//
// AutoCloseCollectionStatus defaults to true when nil. Set to false
// when the caller wants to expire the record at the Memory layer but
// keep the collection item visible in the open view (rare; useful
// when manually staging a multi-step close).
type ResolveRequest struct {
	ID                        string `json:"-" jsonschema:"-"`
	Resolution                string `json:"resolution" jsonschema:"completed|superseded|abandoned|obsolete"`
	ResolutionNote            string `json:"resolution_note,omitempty" jsonschema:"free-form note about why/how"`
	AutoCloseCollectionStatus *bool  `json:"auto_close_collection_status,omitempty" jsonschema:"default true; when false, skip flipping the collection item's status field even if the schema has one"`
}

// ResolveResponse confirms the resolution. AutoClosedStatus reports
// any collection items whose schema-declared `status` enum field was
// also flipped to a closed-equivalent value (mapped from the
// resolution verb). CollectionWarning fires when the record is in a
// collection where the auto-close heuristic could not find a matching
// status field or value -- the caller should fall back to
// gramaton_collection_update for those.
type ResolveResponse struct {
	ID                string            `json:"id"`
	Resolved          bool              `json:"resolved"`
	AutoClosedStatus  map[string]string `json:"auto_closed_status,omitempty"`
	CollectionWarning string            `json:"collection_warning,omitempty"`
}

// ResolveDescription is the MCP tool description for gramaton_resolve.
const ResolveDescription = "Mark a record as resolved (completed/superseded/abandoned/obsolete). Auto-sets valid_until so resolved records deprioritize in search. When the record is a collection item AND the collection's schema has an enum field named `status`, also flips that field to a closed-equivalent value (resolved/done/finished/abandoned/etc; first match in the schema's enum wins). Pass auto_close_collection_status=false to skip the collection-layer write."

// closedStatusCandidates returns the ordered list of `status` enum
// values to consider as the "closed" side for a given resolution
// verb. The first candidate present in the schema's enum wins; this
// makes a `[completed, done, finished, resolved, closed]` template
// pick its own canonical value rather than shadowing it.
//
// completed + superseded share the "this is done positively"
// vocabulary; abandoned + obsolete share the "this is dropped"
// vocabulary. The match is case-insensitive at lookup time.
func closedStatusCandidates(resolution string) []string {
	switch resolution {
	case "completed", "superseded":
		return []string{"completed", "done", "finished", "resolved", "closed"}
	case "abandoned", "obsolete":
		return []string{"abandoned", "cancelled", "canceled", "dropped"}
	}
	return nil
}

// inferClosedStatus returns the value to write into the schema's
// enum-typed `status` field for the given resolution, or "" when no
// match is found. Empty signals: caller should emit a
// CollectionWarning so the operator falls back to
// gramaton_collection_update for this collection.
//
// Lookup rules:
//   - schema must have a field whose name matches "status"
//     (case-insensitive)
//   - that field must be of type enum (enum[] is intentionally
//     excluded; auto-flipping a multi-select feels wrong without an
//     explicit semantic from the schema)
//   - the first closedStatusCandidates entry whose lowercase form
//     appears in the enum's values wins
func inferClosedStatus(schema *CollectionSchema, resolution string) string {
	if schema == nil {
		return ""
	}
	var statusField *SchemaField
	for i, f := range schema.Fields {
		if strings.EqualFold(f.Name, "status") && f.Type == FieldTypeEnum {
			statusField = &schema.Fields[i]
			break
		}
	}
	if statusField == nil {
		return ""
	}
	enumLower := make(map[string]string, len(statusField.Values))
	for _, v := range statusField.Values {
		enumLower[strings.ToLower(v)] = v
	}
	for _, c := range closedStatusCandidates(resolution) {
		if v, ok := enumLower[c]; ok {
			return v
		}
	}
	return ""
}

// Resolve marks a record and auto-sets valid_until so search
// deprioritizes the record going forward. Repeated calls with
// different resolutions overwrite; resolution_note is set only when
// provided.
//
// When the record is a collection item, also flips the collection's
// `status` enum field (when present) to a closed-equivalent value.
// See inferClosedStatus for the heuristic.
func (a *API) Resolve(ctx context.Context, req ResolveRequest) (ResolveResponse, *APIError) {
	if apiErr := a.rejectIfReadOnly("resolve"); apiErr != nil {
		return ResolveResponse{}, apiErr
	}
	if req.ID == "" {
		return ResolveResponse{}, ErrMissing("id is required")
	}
	if req.Resolution == "" {
		return ResolveResponse{}, ErrMissing("resolution is required")
	}
	if err := validateEnum("resolution", req.Resolution, ValidResolutions); err != nil {
		return ResolveResponse{}, ErrInvalid(err.Error())
	}
	if len(req.ResolutionNote) > MaxContextFieldLen {
		return ResolveResponse{}, ErrInvalid(fmt.Sprintf("resolution_note exceeds maximum length of %d", MaxContextFieldLen))
	}

	autoClose := true
	if req.AutoCloseCollectionStatus != nil {
		autoClose = *req.AutoCloseCollectionStatus
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	if _, ok := a.engine.Graph().GetNode(req.ID); !ok {
		return ResolveResponse{}, ErrNotFound("record not found")
	}

	now := time.Now().UTC()
	a.engine.SetProp(req.ID, "resolution", graph.StringProperty(req.Resolution))
	a.engine.SetProp(req.ID, "resolved_at", graph.TimestampProperty(now))
	if req.ResolutionNote != "" {
		a.engine.SetProp(req.ID, "resolution_note", graph.StringProperty(req.ResolutionNote))
	}

	// Auto-set valid_until only when not already set. Prevents a
	// re-resolve from bumping the expiration forward (which would
	// bring a historical record back to "Current" in the search UI).
	n, _ := a.engine.Graph().GetNode(req.ID)
	if _, hasVU := n.Properties.GetTimestamp("valid_until"); !hasVU {
		a.engine.SetProp(req.ID, "valid_until", graph.TimestampProperty(now))
	}

	// Walk member_of edges and decide per-collection whether to
	// auto-flip the status field. autoClosedByName tracks successful
	// flips; warnUnflipped tracks collections where the heuristic
	// couldn't find a target so the caller still gets the manual-path
	// nudge.
	var autoClosedByName map[string]string
	var warnUnflipped []string
	for _, e := range a.engine.Graph().EdgesFrom(req.ID) {
		if e.Type != "member_of" {
			continue
		}
		coll, ok := a.engine.Graph().GetNode(e.TargetID)
		if !ok {
			continue
		}
		name, _ := coll.Properties.GetString("collection_name")
		if name == "" {
			continue
		}
		if !autoClose {
			warnUnflipped = append(warnUnflipped, name)
			continue
		}
		schema, err := loadSchema(coll)
		if err != nil {
			a.log.Warn("auto-close: schema load failed",
				"component", "resolve", "collection", name, "err", err)
			warnUnflipped = append(warnUnflipped, name)
			continue
		}
		target := inferClosedStatus(schema, req.Resolution)
		if target == "" {
			warnUnflipped = append(warnUnflipped, name)
			continue
		}
		a.setFieldProps(req.ID, map[string]any{"status": target})
		if autoClosedByName == nil {
			autoClosedByName = make(map[string]string)
		}
		autoClosedByName[name] = target
	}

	if _, err := a.engine.Save("resolve", graph.CommitAction{
		Kind: graph.ActionResolve, RecordID: req.ID,
	}); err != nil {
		return ResolveResponse{}, ErrInternal("failed to save")
	}

	resp := ResolveResponse{ID: req.ID, Resolved: true}
	if len(autoClosedByName) > 0 {
		resp.AutoClosedStatus = autoClosedByName
	}
	if len(warnUnflipped) > 0 {
		resp.CollectionWarning = fmt.Sprintf(
			"This record is in collection(s) without an auto-closeable status field: %s. Use gramaton_collection_update to set the right status.",
			joinCollectionNames(warnUnflipped))
	}
	return resp, nil
}
