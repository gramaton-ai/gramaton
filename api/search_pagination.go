package api

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// encodeCursor builds the opaque pagination token for the slice
// starting at `start` with `pageSize` results, in the snapshot
// identified by queryID. base64 of "queryID:start:pageSize" --
// not signed (these are pagination state, not security tokens),
// just decoded server-side.
//
// pageSize is encoded so that subsequent calls using this cursor
// see the same page boundaries the original call established. A
// caller's request.PageSize on a cursor call is ignored (and added
// to ignored_params).
func encodeCursor(queryID string, start, pageSize int) string {
	raw := fmt.Sprintf("%s:%d:%d", queryID, start, pageSize)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor reverses encodeCursor. Returns an error on
// malformed input. Length and shape checks only; not auth.
func decodeCursor(cursor string) (queryID string, start, pageSize int, err error) {
	raw, decErr := base64.RawURLEncoding.DecodeString(cursor)
	if decErr != nil {
		return "", 0, 0, fmt.Errorf("invalid cursor encoding")
	}
	parts := strings.SplitN(string(raw), ":", 3)
	if len(parts) != 3 {
		return "", 0, 0, fmt.Errorf("invalid cursor shape")
	}
	s, err := strconv.Atoi(parts[1])
	if err != nil || s < 0 {
		return "", 0, 0, fmt.Errorf("invalid cursor start")
	}
	ps, err := strconv.Atoi(parts[2])
	if err != nil || ps <= 0 {
		return "", 0, 0, fmt.Errorf("invalid cursor page_size")
	}
	return parts[0], s, ps, nil
}

// buildPageTable enumerates the snapshot in pageSize-sized slices
// and returns a PageRef for each. Ranges are 1-indexed for human
// readability ("1-20", "21-40", ...). Each cursor encodes the
// start offset + pageSize so subsequent cursor calls preserve
// page boundaries. Empty snapshots return nil.
func buildPageTable(queryID string, total, pageSize int) []PageRef {
	if total <= 0 || pageSize <= 0 {
		return nil
	}
	pageCount := (total + pageSize - 1) / pageSize
	out := make([]PageRef, 0, pageCount)
	for p := 0; p < pageCount; p++ {
		start := p * pageSize
		end := start + pageSize
		if end > total {
			end = total
		}
		out = append(out, PageRef{
			Range:  fmt.Sprintf("%d-%d", start+1, end),
			Cursor: encodeCursor(queryID, start, pageSize),
		})
	}
	return out
}

// ignoredParamsForCursor reports which request fields were dropped
// because a Cursor was provided. Heuristic, not exhaustive --
// covers the obvious filter / scoring args agents typically supply
// alongside text. Returns nil when no documented "would-have-been-
// used" args are set.
func ignoredParamsForCursor(req SearchRequest) []string {
	var out []string
	if req.Text != "" {
		out = append(out, "text")
	}
	if req.Match != "" {
		out = append(out, "match")
	}
	if req.Temporality != "" {
		out = append(out, "temporality")
	}
	if req.KnowledgeType != "" {
		out = append(out, "knowledge_type")
	}
	if req.EpistemicStatus != "" {
		out = append(out, "epistemic_status")
	}
	if req.Resolution != "" {
		out = append(out, "resolution")
	}
	if req.ProcessingStatus != "" {
		out = append(out, "processing_status")
	}
	if len(req.Keywords) > 0 {
		out = append(out, "keywords")
	}
	if len(req.Missing) > 0 {
		out = append(out, "missing")
	}
	if len(req.Meta) > 0 {
		out = append(out, "meta")
	}
	if req.SimilarTo != "" {
		out = append(out, "similar_to")
	}
	if req.NearNode != "" {
		out = append(out, "near_node")
	}
	if req.Sort != "" {
		out = append(out, "sort")
	}
	if req.Random {
		out = append(out, "random")
	}
	if req.Store != "" {
		out = append(out, "store")
	}
	if req.Top > 0 {
		out = append(out, "top")
	}
	return out
}
