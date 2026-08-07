package core

import (
	"log/slog"

	"github.com/gramaton-ai/gramaton/similarity"
)

// recentWritesCap bounds the delta re-scan ring. The ring only needs
// to cover records committed inside a single save's scan-to-lock
// window (milliseconds to low seconds under embedder load), so the
// cap is far above any realistic concurrent write burst. When the
// ring has wrapped past a caller's sequence number the delta check
// reports incomplete coverage and the save proceeds -- a bounded
// residual race no worse than the historical off-lock scan, taken in
// preference to running a full index scan under the write lock.
const recentWritesCap = 512

// recentWrite is one entry in the delta re-scan ring: a node that
// committed with an embedding, in commit order.
type recentWrite struct {
	seq    uint64
	nodeID string
	vec    []float32
}

// WriteSeq returns the current write sequence number. A save-guard
// caller captures this under the read lock BEFORE running the
// off-lock similarity scan, then passes it to SimilarInDelta under
// the write lock; every embedding-bearing node committed in between
// carries a higher sequence and is re-checked.
// Caller must hold at least a read lock.
func (e *Engine) WriteSeq() uint64 {
	return e.recentSeq
}

// NoteRecentWrite records a just-committed embedding-bearing node in
// the delta re-scan ring. Node-creation sites call this under the
// write lock, after the vector is attached.
// Caller must hold the write lock.
func (e *Engine) NoteRecentWrite(nodeID string, vec []float32) {
	if vec == nil {
		return
	}
	e.recentSeq++
	e.recentWrites = append(e.recentWrites, recentWrite{seq: e.recentSeq, nodeID: nodeID, vec: vec})
	if len(e.recentWrites) > recentWritesCap {
		e.recentWrites = e.recentWrites[len(e.recentWrites)-recentWritesCap:]
	}
}

// SimilarInDelta re-checks the records committed since sinceSeq
// against a not-yet-inserted record's embedding and content,
// returning the best hold-qualifying match if any. complete is false
// when the ring wrapped past sinceSeq (coverage gap -- callers log
// and proceed; see recentWritesCap).
// Caller must hold the write lock.
func (e *Engine) SimilarInDelta(sinceSeq uint64, vec []float32, content string) (match similarity.Match, found bool, complete bool) {
	complete = true
	if len(e.recentWrites) > 0 && e.recentWrites[0].seq > sinceSeq+1 {
		complete = false
		slog.Warn("save-guard delta re-scan: ring wrapped, coverage incomplete",
			"component", "engine",
			"since_seq", sinceSeq,
			"oldest_retained", e.recentWrites[0].seq)
	}
	best := similarity.Match{}
	for i := len(e.recentWrites) - 1; i >= 0; i-- {
		rw := e.recentWrites[i]
		if rw.seq <= sinceSeq {
			break
		}
		sim, holds := similarity.MatchAgainst(e.graph, e.cfg.SaveGuard, vec, content, rw.nodeID)
		if holds && sim > best.Similarity {
			best = similarity.Match{NodeID: rw.nodeID, Similarity: sim}
		}
	}
	if best.NodeID != "" {
		return best, true, complete
	}
	return similarity.Match{}, false, complete
}
