package migrate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// ArchiveRecord is one victim's forever-insurance line in the JSONL
// archive, written before any deletion. It survives GC truncation of
// the commit history; after that horizon it is the only copy.
type ArchiveRecord struct {
	ArchivedAt     time.Time      `json:"archived_at"`
	VictimID       string         `json:"victim_id"`
	SuccessorID    string         `json:"successor_id"`
	EdgeWeight     float64        `json:"edge_weight"`
	Properties     map[string]any `json:"properties"`
	Collections    []string       `json:"collections,omitempty"`
	SegmentIDs     []string       `json:"segment_ids,omitempty"`
	ObservationIDs []string       `json:"observation_ids,omitempty"`
}

// ApplyResult reports what one collapse run changed.
type ApplyResult struct {
	VictimsDeleted      int    `json:"victims_deleted"`
	VictimsSkipped      int    `json:"victims_skipped"`
	SegmentsRepointed   int    `json:"segments_repointed"`
	ObservationsDeleted int    `json:"observations_deleted"`
	ArchivePath         string `json:"archive_path"`
}

// Apply executes a plan's victims: archive first, then re-point
// provenance, cascade observation children, and hard-delete each
// victim -- all mutations in one labeled commit. The archive file
// must not already exist (a re-run gets a fresh path; overwriting
// forever-insurance is never correct). Victims whose selection props
// changed between plan and apply are skipped and counted, never
// deleted on stale evidence.
func Apply(eng *core.Engine, plan *Plan, archivePath string) (*ApplyResult, error) {
	if len(plan.Victims) == 0 {
		return &ApplyResult{ArchivePath: ""}, nil
	}

	f, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create archive %s: %w", archivePath, err)
	}

	// Archive phase, under the read lock: every victim's full record
	// lands on disk and syncs before the first mutation.
	w := bufio.NewWriter(f)
	archived := map[string]bool{}
	eng.RLock()
	now := time.Now().UTC()
	for _, v := range plan.Victims {
		n, ok := eng.Graph().GetNode(v.ID)
		if !ok {
			continue
		}
		rec := ArchiveRecord{
			ArchivedAt:     now,
			VictimID:       v.ID,
			SuccessorID:    v.SuccessorID,
			EdgeWeight:     v.EdgeWeight,
			Properties:     make(map[string]any, len(n.Properties)),
			Collections:    v.Collections,
			SegmentIDs:     v.SegmentIDs,
			ObservationIDs: v.ObservationIDs,
		}
		for k, p := range n.Properties {
			// Embeddings are re-derivable and model-bound; the
			// archive keeps knowledge, not vectors.
			if strings.HasPrefix(k, "embedding_") {
				continue
			}
			rec.Properties[k] = p.FormatValue()
		}
		line, err := json.Marshal(rec)
		if err != nil {
			eng.RUnlock()
			f.Close()
			return nil, fmt.Errorf("marshal archive record %s: %w", v.ID, err)
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			eng.RUnlock()
			f.Close()
			return nil, fmt.Errorf("write archive: %w", err)
		}
		archived[v.ID] = true
	}
	eng.RUnlock()
	if err := w.Flush(); err != nil {
		f.Close()
		return nil, fmt.Errorf("flush archive: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, fmt.Errorf("sync archive: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close archive: %w", err)
	}

	// Mutation phase: one batched commit. Re-pointing happens before
	// the victim's deletion cascades the old provenance edge away;
	// each victim's resolution and valid_until re-verify under the
	// write lock so nothing is deleted on stale plan evidence.
	res := &ApplyResult{ArchivePath: archivePath}
	err = eng.WithWriteBatch("backfill collapse-superseded", func(ws *core.WriteSession) (bool, error) {
		g := ws.Graph()
		// Edges added earlier in this batch are invisible to the
		// committed-state EdgesFrom scan below; track them here so a
		// segment shared by two victims with the same successor does
		// not get a duplicate provenance edge.
		addedEdge := map[string]bool{}
		for _, v := range plan.Victims {
			if !archived[v.ID] {
				res.VictimsSkipped++
				continue
			}
			n, ok := g.GetNode(v.ID)
			if !ok {
				res.VictimsSkipped++
				continue
			}
			if resl, _ := n.Properties.GetString("resolution"); resl != "superseded" {
				res.VictimsSkipped++
				continue
			}
			if _, hasVU := n.Properties.GetTimestamp("valid_until"); !hasVU {
				res.VictimsSkipped++
				continue
			}
			if _, ok := g.GetNode(v.SuccessorID); !ok {
				res.VictimsSkipped++
				continue
			}

			for _, segID := range v.SegmentIDs {
				seg, ok := g.GetNode(segID)
				if !ok {
					continue
				}
				pairKey := segID + "\x00" + v.SuccessorID
				already := addedEdge[pairKey]
				if !already {
					for _, e := range g.EdgesFrom(segID) {
						if e.Type == "extracted_as" && e.TargetID == v.SuccessorID {
							already = true
							break
						}
					}
				}
				if !already {
					if _, err := ws.AddEdge(segID, v.SuccessorID, "extracted_as", 1.0, nil); err != nil {
						return false, fmt.Errorf("re-point segment %s: %w", segID, err)
					}
					addedEdge[pairKey] = true
					res.SegmentsRepointed++
				}
				// captured_as may legitimately point at a different
				// still-live record; only redirect it away from the
				// victim being deleted.
				if captured, _ := seg.Properties.GetString("captured_as"); captured == v.ID {
					ws.SetProp(segID, "captured_as", graph.StringProperty(v.SuccessorID))
				}
			}

			for _, obsID := range v.ObservationIDs {
				if _, ok := g.GetNode(obsID); !ok {
					continue
				}
				if err := ws.DeleteNode(obsID); err != nil {
					return false, fmt.Errorf("cascade observation %s: %w", obsID, err)
				}
				res.ObservationsDeleted++
			}

			if err := ws.DeleteNode(v.ID); err != nil {
				return false, fmt.Errorf("delete victim %s: %w", v.ID, err)
			}
			res.VictimsDeleted++
		}
		if res.VictimsDeleted > 0 {
			ws.AddAction(graph.CommitAction{Kind: graph.ActionBackfill, Field: "collapse_superseded"})
		}
		return res.VictimsDeleted > 0, nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
