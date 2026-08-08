package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/graph"
)

// BranchEntry is one branch in a list response. Hash is truncated
// for display consistency with gramaton_log.
type BranchEntry struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
	Active bool   `json:"active,omitempty"`
}

type BranchListResponse struct {
	Branches []BranchEntry `json:"branches"`
	Current  string        `json:"current"`
}

type BranchCreateRequest struct {
	Name string `json:"name" jsonschema:"branch name (see ValidBranchName)"`
}

type BranchCreateResponse struct {
	Name    string `json:"name"`
	Commit  string `json:"commit"`
	Created bool   `json:"created"`
}

type BranchCheckoutResponse struct {
	Name       string `json:"name"`
	Commit     string `json:"commit"`
	CheckedOut bool   `json:"checked_out"`
}

type BranchMergeResponse struct {
	Merged    string `json:"merged"`
	NewCommit string `json:"new_commit"`
}

type BranchDiscardResponse struct {
	Discarded string `json:"discarded"`
}

const (
	BranchListDescription     = "List all branches with their commit hashes and which one is active."
	BranchCreateDescription   = "Create a new branch at the current HEAD. Use for safe experimentation before merging into main."
	BranchCheckoutDescription = "Switch to an existing branch. The graph is loaded from the branch's commit off-lock; indexes are rebuilt under lock."
	BranchMergeDescription    = "Merge a branch into main (fast-forward). Deletes the branch ref on success."
	BranchDiscardDescription  = "Discard a branch without merging. Switches back to main if the discarded branch was active."
)

// BranchList returns every ref plus the active branch.
func (a *API) BranchList(ctx context.Context) (BranchListResponse, *APIError) {
	a.engine.RLock()
	defer a.engine.RUnlock()

	dataDir := a.engine.Config().DataDir
	active := core.ActiveBranch(dataDir)
	entries, err := os.ReadDir(core.RefsDir(dataDir))
	if err != nil {
		// Missing refs dir == no branches yet. Return the active
		// name so the caller sees the store isn't broken.
		return BranchListResponse{Branches: []BranchEntry{}, Current: active}, nil
	}

	resp := BranchListResponse{Branches: []BranchEntry{}, Current: active}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		hash, _ := core.ReadRef(dataDir, e.Name())
		entry := BranchEntry{Name: e.Name(), Commit: core.TruncHash(hash)}
		if e.Name() == active {
			entry.Active = true
		}
		resp.Branches = append(resp.Branches, entry)
	}
	return resp, nil
}

// BranchCreate writes a new ref at the current HEAD. Short lock
// hold -- one ReadRef + one WriteRef -- so no off-lock split.
func (a *API) BranchCreate(ctx context.Context, req BranchCreateRequest) (BranchCreateResponse, *APIError) {
	if apiErr := a.rejectIfReadOnly("branch_create"); apiErr != nil {
		return BranchCreateResponse{}, apiErr
	}
	if err := core.ValidBranchName(req.Name); err != nil {
		return BranchCreateResponse{}, ErrInvalid(err.Error())
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	dataDir := a.engine.Config().DataDir
	if _, err := core.ReadRef(dataDir, req.Name); err == nil {
		return BranchCreateResponse{}, ErrConflict(fmt.Sprintf("branch %q already exists", req.Name))
	}

	headHash := a.engine.HeadHashLocked()
	if err := core.WriteRef(dataDir, req.Name, headHash); err != nil {
		a.log.Warn("branch create: write ref failed", "component", "branch", "name", req.Name, "err", err)
		return BranchCreateResponse{}, ErrInternal("failed to create branch")
	}
	return BranchCreateResponse{
		Name:    req.Name,
		Commit:  core.TruncHash(headHash),
		Created: true,
	}, nil
}

// BranchCheckout loads the branch's committed graph state.
// Three-phase lock discipline:
//  1. RLock: read the target ref hash, release.
//  2. No lock: stage the committed graph via graph.LoadStaged
//     (memory-backed edges, always loaded from the commit's tree).
//  3. Lock: write HEAD + active branch FIRST (so a partial failure
//     leaves no in-memory/on-disk divergence), then AdoptGraph
//     (staged edges replace the shared bbolt store's contents and
//     the store transfers to the new graph), then rebuild indexes.
func (a *API) BranchCheckout(ctx context.Context, name string) (BranchCheckoutResponse, *APIError) {
	// Checkout doesn't change knowledge content but rewrites HEAD and
	// BRANCH in the data dir and swaps the live graph -- mutations of
	// the frozen artifact all the same.
	if apiErr := a.rejectIfReadOnly("branch_checkout"); apiErr != nil {
		return BranchCheckoutResponse{}, apiErr
	}
	if err := core.ValidBranchName(name); err != nil {
		return BranchCheckoutResponse{}, ErrInvalid(err.Error())
	}

	// Phase 1: snapshot ref hash.
	a.engine.RLock()
	dataDir := a.engine.Config().DataDir
	hash, err := core.ReadRef(dataDir, name)
	a.engine.RUnlock()
	if err != nil {
		return BranchCheckoutResponse{}, ErrNotFound(fmt.Sprintf("branch %q not found", name))
	}

	// Phase 2: parse the target commit off-lock into a STAGED graph
	// (memory-backed edges). Loading straight into a graph that
	// shares the engine's populated bbolt edge store would make Load
	// skip edge reload entirely and keep the OLD branch's edges under
	// the new branch's nodes.
	newGraph, _, err := graph.LoadStaged(a.engine.Store(), hash)
	if err != nil {
		a.log.Warn("branch checkout: graph load failed", "component", "branch", "name", name, "err", err)
		return BranchCheckoutResponse{}, ErrInternal("failed to load branch state")
	}

	// Phase 3: persist on-disk pointers FIRST, then adopt the staged
	// graph. If HEAD or active-branch writes fail, we abort without
	// touching the in-memory engine state -- the user's view is the
	// pre-checkout branch and a retry is idempotent. AdoptGraph then
	// replaces the shared edge store's contents with the staged edge
	// set and hands the store to the new graph so post-checkout edge
	// writes persist.
	a.engine.Lock()
	defer a.engine.Unlock()

	headPath := filepath.Join(dataDir, "HEAD")
	if err := core.AtomicWriteFile(headPath, []byte(hash), 0o600); err != nil {
		a.log.Warn("branch checkout: write HEAD failed", "component", "branch", "name", name, "err", err)
		return BranchCheckoutResponse{}, ErrInternal("failed to update HEAD")
	}
	if err := core.SetActiveBranch(dataDir, name); err != nil {
		a.log.Warn("branch checkout: set active failed", "component", "branch", "name", name, "err", err)
		return BranchCheckoutResponse{}, ErrInternal("failed to set active branch")
	}
	a.engine.AdoptGraph(newGraph)
	a.engine.RebuildAllIndexes()

	return BranchCheckoutResponse{
		Name:       name,
		Commit:     core.TruncHash(hash),
		CheckedOut: true,
	}, nil
}

// BranchMerge fast-forwards main to absorb the named branch, then
// deletes the branch ref. Same off-lock parse pattern as checkout;
// the new graph shares the engine's BboltEdgeStore so post-merge
// edge writes persist.
//
// Note: engine.Save() writes HEAD as part of its commit path, so
// merge's "swap then save" sequence ends with HEAD pointing at the
// new merge commit. main ref is updated after Save -- if that ref
// write fails the new commit exists in the object store and HEAD
// points at it (recoverable: re-running merge will pick up where
// we left off, since the graph is still in main's state).
func (a *API) BranchMerge(ctx context.Context, name string) (BranchMergeResponse, *APIError) {
	if apiErr := a.rejectIfReadOnly("branch_merge"); apiErr != nil {
		return BranchMergeResponse{}, apiErr
	}
	if err := core.ValidBranchName(name); err != nil {
		return BranchMergeResponse{}, ErrInvalid(err.Error())
	}
	if name == "main" {
		return BranchMergeResponse{}, ErrInvalid("cannot merge main into itself")
	}

	// Phase 1: snapshot branch hash under read lock.
	a.engine.RLock()
	dataDir := a.engine.Config().DataDir
	branchHash, err := core.ReadRef(dataDir, name)
	a.engine.RUnlock()
	if err != nil {
		return BranchMergeResponse{}, ErrNotFound(fmt.Sprintf("branch %q not found", name))
	}

	// Phase 2: parse the branch commit off-lock into a STAGED graph
	// (memory-backed edges) -- same corruption guard as checkout: a
	// shared populated edge store would short-circuit edge reload.
	newGraph, _, err := graph.LoadStaged(a.engine.Store(), branchHash)
	if err != nil {
		a.log.Warn("branch merge: graph load failed", "component", "branch", "name", name, "err", err)
		return BranchMergeResponse{}, ErrInternal("failed to load branch state")
	}

	// Phase 3: switch active branch first (cheap on-disk write),
	// then adopt the staged graph, rebuild indexes, and commit.
	a.engine.Lock()
	defer a.engine.Unlock()

	if err := core.SetActiveBranch(dataDir, "main"); err != nil {
		a.log.Warn("branch merge: switch to main failed", "component", "branch", "name", name, "err", err)
		return BranchMergeResponse{}, ErrInternal("failed to switch to main")
	}

	a.engine.AdoptGraph(newGraph)
	a.engine.RebuildAllIndexes()

	commit, err := a.engine.Save(fmt.Sprintf("merge branch %q", name), graph.CommitAction{
		Kind: graph.ActionMerge,
	})
	if err != nil {
		a.log.Warn("branch merge: save failed", "component", "branch", "name", name, "err", err)
		return BranchMergeResponse{}, ErrInternal("failed to save merge")
	}
	if err := core.WriteRef(dataDir, "main", commit.Hash); err != nil {
		a.log.Warn("branch merge: write main ref failed", "component", "branch", "name", name, "err", err)
		return BranchMergeResponse{}, ErrInternal("failed to update main ref")
	}
	if err := core.DeleteRef(dataDir, name); err != nil {
		// Non-fatal: merge succeeded but branch cleanup failed.
		a.log.Warn("branch ref cleanup failed after merge", "component", "branch", "name", name, "err", err)
	}

	a.log.Info("branch merged", "component", "branch", "name", name, "commit", core.TruncHash(commit.Hash))
	return BranchMergeResponse{
		Merged:    name,
		NewCommit: core.TruncHash(commit.Hash),
	}, nil
}

// BranchDiscard deletes a branch ref without merging. If the
// discarded branch is the active one, switches HEAD + active back
// to main BEFORE deleting -- otherwise we'd leave HEAD pointing at
// a missing ref. Short lock hold -- only on-disk writes.
func (a *API) BranchDiscard(ctx context.Context, name string) (BranchDiscardResponse, *APIError) {
	if apiErr := a.rejectIfReadOnly("branch_discard"); apiErr != nil {
		return BranchDiscardResponse{}, apiErr
	}
	if err := core.ValidBranchName(name); err != nil {
		return BranchDiscardResponse{}, ErrInvalid(err.Error())
	}
	if name == "main" {
		return BranchDiscardResponse{}, ErrInvalid("cannot discard main")
	}

	a.engine.Lock()
	defer a.engine.Unlock()

	dataDir := a.engine.Config().DataDir
	if _, err := core.ReadRef(dataDir, name); err != nil {
		return BranchDiscardResponse{}, ErrNotFound(fmt.Sprintf("branch %q not found", name))
	}

	// If we're discarding the active branch, the on-disk HEAD must
	// be moved to main BEFORE the ref is deleted -- a failure here
	// is fatal because deleting the ref would orphan HEAD.
	if core.ActiveBranch(dataDir) == name {
		mainHash, err := core.ReadRef(dataDir, "main")
		if err != nil {
			a.log.Warn("branch discard: read main ref failed",
				"component", "branch", "name", name, "err", err)
			return BranchDiscardResponse{}, ErrInternal("failed to read main ref while switching off discarded branch")
		}
		headPath := filepath.Join(dataDir, "HEAD")
		if err := core.AtomicWriteFile(headPath, []byte(mainHash), 0o600); err != nil {
			a.log.Warn("branch discard: write HEAD failed",
				"component", "branch", "name", name, "err", err)
			return BranchDiscardResponse{}, ErrInternal("failed to update HEAD while switching off discarded branch")
		}
		if err := core.SetActiveBranch(dataDir, "main"); err != nil {
			a.log.Warn("branch discard: set main active failed",
				"component", "branch", "name", name, "err", err)
			return BranchDiscardResponse{}, ErrInternal("failed to set active branch while switching off discarded branch")
		}
	}
	if err := core.DeleteRef(dataDir, name); err != nil {
		// Non-fatal: HEAD/active are correct; ref cleanup just lingers.
		a.log.Warn("branch discard: delete ref failed",
			"component", "branch", "name", name, "err", err)
	}

	a.log.Info("branch discarded", "component", "branch", "name", name)
	return BranchDiscardResponse{Discarded: name}, nil
}
