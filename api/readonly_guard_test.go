package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// Read-only guard categories. Every exported method on *API must be
// classified in readOnlyClassification below.
//
//   - guardWrite: the method mutates store state (graph nodes/edges,
//     record properties, HEAD/refs, the data dir) and MUST call
//     a.rejectIfReadOnly as part of its body so a frozen store
//     rejects it with code "forbidden" before any mutation.
//   - guardRead: the method only reads store state (or writes strictly
//     OUTSIDE the data dir, like backup create/export) and must NOT
//     carry the guard -- frozen stores stay fully readable, and export
//     is how they are shared.
//   - guardInfra: accessor / lifecycle plumbing (not an operation);
//     must not carry the guard.
const (
	guardWrite = "WRITE"
	guardRead  = "READ"
	guardInfra = "INFRA"
)

// readOnlyClassification is the audit trail for the store-level
// read-only decision, one entry per exported method on *API. Entries
// carry a comment when the classification is not obvious from the
// method name.
//
// MANUAL INVARIANT: server/mcp_readonly.go carries the MCP-facing
// twin of this classification, keyed by tool name (an MCP tool is
// write iff any operation reachable through it is guardWrite here).
// When reclassifying a method or adding an operation, keep the two
// consistent -- there is no automated cross-check because this map
// lives in a test file.
var readOnlyClassification = map[string]string{
	// Records cluster.
	"Save":         guardWrite,
	"SaveBatch":    guardWrite, // guard at the shared entry covers sync + async/chunked runners
	"Update":       guardWrite,
	"DeleteRecord": guardWrite,
	"Link":         guardWrite,
	"Unlink":       guardWrite,
	"Resolve":      guardWrite,
	"Classify":     guardWrite,
	"Reembed":      guardWrite,

	// Async-batch job plumbing. Status/Result only read jobs.db.
	// Cancel WRITES jobs.db -- but jobs.db is a derived local cache
	// that stays writable on a frozen store by design (layer-1
	// semantics: only the knowledge is frozen), no batch can START on
	// a frozen store, and cancel of a leftover pre-freeze job is
	// idempotent control-state cleanup, not knowledge mutation.
	"SaveBatchStatus": guardRead,
	"SaveBatchResult": guardRead,
	"SaveBatchCancel": guardRead,

	// Collections cluster.
	"CollectionCreate":       guardWrite,
	"CollectionAdd":          guardWrite,
	"CollectionAddBatch":     guardWrite,
	"CollectionRemove":       guardWrite,
	"CollectionUpdate":       guardWrite,
	"CollectionMove":         guardWrite,
	"CollectionRename":       guardWrite,
	"CollectionDelete":       guardWrite, // retire AND unretire branches both commit
	"CollectionSchemaUpdate": guardWrite,
	"CollectionMigrate":      guardWrite,
	"CollectionList":         guardRead,
	"CollectionItems":        guardRead,
	"CollectionSchemaRead":   guardRead,

	// Sessions cluster. Prepare is read-shaped but is the entry to
	// the two-phase write flow; archive is read-shaped but stamps
	// archive_* props and commits.
	"SessionStart":       guardWrite,
	"SessionPrepare":     guardWrite,
	"SessionSave":        guardWrite,
	"SessionResolveHeld": guardWrite,
	"SessionArchive":     guardWrite,
	"SessionGet":         guardRead,

	// Branches cluster. Checkout rewrites HEAD/BRANCH in the data
	// dir even though it does not change knowledge content.
	"BranchCreate":   guardWrite,
	"BranchCheckout": guardWrite,
	"BranchMerge":    guardWrite,
	"BranchDiscard":  guardWrite,
	"BranchList":     guardRead,

	// Backup cluster. Create and Export only READ the store and
	// write outside the data dir -- exporting/backing up a frozen
	// store must keep working (that is how it is shared). Restore
	// replaces the data dir; Import creates records.
	"BackupStatus":  guardRead,
	"BackupCreate":  guardRead,
	"BackupExport":  guardRead,
	"BackupRestore": guardWrite,
	"BackupImport":  guardWrite,

	// CarveOut READS the source store and writes only to a BRAND-NEW
	// destination store (its own data dir, outside the source's) --
	// same rationale as BackupCreate/Export. Carving a shareable subset
	// out of a frozen store is a primary use case, so it must NOT be
	// gated behind the source's read-only flag.
	"CarveOut": guardRead,

	// CarveAdd, like CarveOut, READS the source store and writes only to
	// a SEPARATE existing destination store (its own data dir). It
	// manages the DESTINATION's freeze state (thaw/refreeze), never the
	// source's, so it is not gated behind the source's read-only flag.
	"CarveAdd": guardRead,

	// Curation cluster. DryRun still APPLIES the deterministic
	// phase; Drain writes no_contradiction edges; Batch classifies
	// (writes) every pending record.
	"CurationTrigger":             guardWrite,
	"CurationDryRun":              guardWrite,
	"CurationBatch":               guardWrite,
	"CurationDrainContradictions": guardWrite,
	"CurationResetStuck":          guardWrite,
	"CurationStatus":              guardRead,
	"CurationListStuck":           guardRead,

	// Read-only operations.
	"Search":        guardRead, // skips its access bump when frozen (see api/search.go)
	"Inspect":       guardRead, // skips its access bump when frozen (see api/inspect.go)
	"Explore":       guardRead,
	"Stats":         guardRead,
	"Status":        guardRead,
	"Pending":       guardRead,
	"Duplicates":    guardRead,
	"Guide":         guardRead,
	"History":       guardRead,
	"HistorySearch": guardRead,
	"Log":           guardRead,
	"Diff":          guardRead,
	"JobsList":      guardRead,

	// Accessors and lifecycle plumbing (not operations).
	"Engine":                       guardInfra,
	"UsageTracker":                 guardInfra,
	"Runner":                       guardInfra,
	"SetRunner":                    guardInfra,
	"Logger":                       guardInfra,
	"ConfigDir":                    guardInfra,
	"SetBackupSnapshotHook":        guardInfra,
	"SetHistorySearchSnapshotHook": guardInfra,
	"SetFaultInjector":             guardInfra,
	"SetChunkSizeForTests":         guardInfra,
	"StopPreparedSweeper":          guardInfra,
	"StartPreparedSweeper":         guardInfra,
	"ShutdownAsync":                guardInfra,
}

// TestReadOnlyGuardCoversEveryAPIMethod pins the store-level
// read-only contract: every exported method on api.API has made an
// explicit WRITE / READ decision, and every WRITE method calls
// a.rejectIfReadOnly in its body (so a frozen store rejects it with
// code "forbidden" before any lock acquisition or in-memory graph
// mutation).
//
// If this test fails because a NEW exported method appeared:
//  1. Decide whether the method mutates store state. Anything that
//     calls engine Save/WithWriteBatch/SetProp/SetContentProp/
//     IndexNode, or mutates via engine.Graph() (AddNode, AddEdge,
//     SetNodeProperty, DeleteNode, RemoveNodeProperty, DeleteEdge,
//     ...), or writes files inside the data dir, is a WRITE.
//  2. WRITE: add `if apiErr := a.rejectIfReadOnly("<op>"); apiErr !=
//     nil { return ..., apiErr }` as the method's FIRST statement
//     (see api/save.go for the canonical form), then add the method
//     to the map as guardWrite.
//  3. READ: add it as guardRead. Do NOT add the guard -- frozen
//     stores must stay fully readable.
//
// Never add an entry without deciding the category -- the map is the
// audit trail for which operations a frozen store refuses and why.
//
// Residual limits, acknowledged: the check is per-method presence of
// the guard CALL, via AST. It cannot prove the guard runs before the
// method's first mutation (the wiring tests in readonly_api_test.go
// cover representative behavior), and operations that live outside
// api.API (server/handler_ops.go revert+ingest, the intake service)
// carry their own inline guards outside this tripwire's reach.
func TestReadOnlyGuardCoversEveryAPIMethod(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	// method name -> whether its body calls rejectIfReadOnly.
	found := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 {
				continue
			}
			if receiverTypeName(fd.Recv.List[0].Type) != "API" {
				continue
			}
			if !fd.Name.IsExported() {
				continue
			}
			if _, dup := found[fd.Name.Name]; dup {
				t.Errorf("duplicate exported method %s on API", fd.Name.Name)
			}
			found[fd.Name.Name] = bodyCallsRejectIfReadOnly(fd.Body)
		}
	}
	if len(found) == 0 {
		t.Fatal("no exported API methods found -- walk is broken")
	}

	var problems []string
	for name, hasGuard := range found {
		category, ok := readOnlyClassification[name]
		if !ok {
			problems = append(problems, name+": exported API method is not classified -- decide WRITE (add the rejectIfReadOnly guard) or READ, then add it to readOnlyClassification")
			continue
		}
		switch category {
		case guardWrite:
			if !hasGuard {
				problems = append(problems, name+": classified WRITE but its body never calls rejectIfReadOnly -- a frozen store would accept this write")
			}
		case guardRead, guardInfra:
			if hasGuard {
				problems = append(problems, name+": classified "+category+" but calls rejectIfReadOnly -- reads must keep working on a frozen store (reclassify as WRITE if it actually mutates)")
			}
		default:
			problems = append(problems, name+": unknown category "+category)
		}
	}
	for name := range readOnlyClassification {
		if _, ok := found[name]; !ok {
			problems = append(problems, name+": classified but no exported API method with this name exists (stale entry?)")
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Errorf("read-only guard tripwire hit:\n  %s\n\n"+
			"Every exported api.API method must make an explicit read-only\n"+
			"decision. See this test's doc comment for the recipe.",
			strings.Join(problems, "\n  "))
	}
}

// receiverTypeName unwraps *API / API receiver type expressions.
func receiverTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(e.X)
	case *ast.Ident:
		return e.Name
	}
	return ""
}

// bodyCallsRejectIfReadOnly reports whether any call expression in
// the body invokes a selector named rejectIfReadOnly.
func bodyCallsRejectIfReadOnly(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "rejectIfReadOnly" {
			found = true
			return false
		}
		return true
	})
	return found
}
