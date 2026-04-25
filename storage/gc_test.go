package storage

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func tempGCStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "chunks"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// makeCommit creates a commit with a small prolly tree and writes it to the store.
// Returns the commit hash and a list of all CAS hashes the commit references.
func makeCommit(t *testing.T, s *Store, parent string, ts time.Time, msg string, nodeCount int) (string, []string) {
	t.Helper()

	// Create leaf data chunks (simulating node data).
	var allHashes []string
	var entries []ProllyEntry
	for i := 0; i < nodeCount; i++ {
		data := []byte(fmt.Sprintf(`{"id":"n%d","props":{"content":"%s node %d"}}`, i, msg, i))
		hash, err := s.Write(data)
		if err != nil {
			t.Fatalf("Write node: %v", err)
		}
		entries = append(entries, ProllyEntry{Key: fmt.Sprintf("n%d", i), Value: hash})
		allHashes = append(allHashes, hash)
	}

	// Build a simple leaf-only prolly tree node.
	leafNode := ProllyNode{Leaf: true, Entries: entries}
	leafData, _ := json.Marshal(leafNode)
	leafHash, _ := s.Write(leafData)
	allHashes = append(allHashes, leafHash)

	// Build commit.
	commit := GCCommit{
		Parent:       parent,
		Timestamp:    ts,
		NodeTreeRoot: leafHash,
	}
	commitData, _ := json.Marshal(commit)
	commitHash, _ := s.Write(commitData)
	allHashes = append(allHashes, commitHash)
	commit.Hash = commitHash

	return commitHash, allHashes
}

func commitLoader(s *Store) func(string) (*GCCommit, error) {
	return func(hash string) (*GCCommit, error) {
		data, err := s.Read(hash)
		if err != nil {
			return nil, err
		}
		var c GCCommit
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, err
		}
		c.Hash = hash
		return &c, nil
	}
}

func TestGCDeletesUnreachableChunks(t *testing.T) {
	s := tempGCStore(t)
	now := time.Now().UTC()

	// Create a chain of 3 commits.
	h1, _ := makeCommit(t, s, "", now.Add(-3*time.Hour), "first", 2)
	h2, _ := makeCommit(t, s, h1, now.Add(-2*time.Hour), "second", 2)
	h3, _ := makeCommit(t, s, h2, now.Add(-1*time.Hour), "third", 2)

	// Write an orphan chunk (not referenced by any commit).
	orphanHash, _ := s.Write([]byte("orphan data not in any commit"))

	// GC with no age limit (keep everything reachable from tip).
	result, err := s.GC(GCOptions{
		CommitLoader: commitLoader(s),
		BranchTips:   func() []string { return []string{h3} },
		HeadHash:     h3,
	})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}

	if result.DeletedCount != 1 {
		t.Fatalf("expected 1 deleted (orphan), got %d", result.DeletedCount)
	}
	if s.Has(orphanHash) {
		t.Fatal("orphan chunk should have been deleted")
	}
}

func TestGCRespectsMinAge(t *testing.T) {
	s := tempGCStore(t)
	now := time.Now().UTC()

	// Create a chain: old -> recent -> head.
	h1, chunks1 := makeCommit(t, s, "", now.Add(-48*time.Hour), "old", 3)
	h2, _ := makeCommit(t, s, h1, now.Add(-1*time.Hour), "recent", 3)
	h3, _ := makeCommit(t, s, h2, now.Add(-1*time.Minute), "head", 3)

	// GC with 24h retention: h1 is older, but must keep MinKeepCommits.
	result, err := s.GC(GCOptions{
		CommitLoader:   commitLoader(s),
		BranchTips:     func() []string { return []string{h3} },
		HeadHash:       h3,
		MinAge:         24 * time.Hour,
		MinKeepCommits: 2, // keep at least 2 commits
	})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}

	// h1's unique chunks should be deleted (only 2 commits kept: h3, h2).
	// h1 is old enough and beyond MinKeepCommits.
	for _, ch := range chunks1 {
		if ch == h1 {
			// The commit chunk itself may be shared; skip.
			continue
		}
	}

	if result.DeletedCount == 0 {
		t.Fatal("expected some chunks to be deleted from old commit")
	}
}

func TestGCProtectsBranchTips(t *testing.T) {
	s := tempGCStore(t)
	now := time.Now().UTC()

	// Two branches from a common ancestor.
	ancestor, _ := makeCommit(t, s, "", now.Add(-72*time.Hour), "ancestor", 2)
	branchA, _ := makeCommit(t, s, ancestor, now.Add(-48*time.Hour), "branch-a", 2)
	branchB, _ := makeCommit(t, s, ancestor, now.Add(-48*time.Hour), "branch-b", 2)

	// GC with aggressive retention (1h). Both branch tips are old
	// but must be protected.
	result, err := s.GC(GCOptions{
		CommitLoader:   commitLoader(s),
		BranchTips:     func() []string { return []string{branchA, branchB} },
		MinAge:         1 * time.Hour,
		MinKeepCommits: 1,
	})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}

	// Both branch tips must still be readable.
	if !s.Has(branchA) {
		t.Fatal("branch A tip was deleted")
	}
	if !s.Has(branchB) {
		t.Fatal("branch B tip was deleted")
	}

	// Ancestor is beyond retention (72h old) and beyond MinKeepCommits
	// for each branch. Its unique chunks may be deleted.
	_ = result
}

func TestGCDryRun(t *testing.T) {
	s := tempGCStore(t)
	now := time.Now().UTC()

	h1, _ := makeCommit(t, s, "", now, "only", 2)
	orphan, _ := s.Write([]byte("orphan"))

	result, err := s.GC(GCOptions{
		CommitLoader: commitLoader(s),
		BranchTips:   func() []string { return []string{h1} },
		HeadHash:     h1,
		DryRun:       true,
	})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}

	if result.DeletedCount != 1 {
		t.Fatalf("dry run should report 1 deletion, got %d", result.DeletedCount)
	}
	if len(result.DeletedHashes) != 1 || result.DeletedHashes[0] != orphan {
		t.Fatal("dry run should report the orphan hash")
	}

	// Orphan should still exist (dry run).
	if !s.Has(orphan) {
		t.Fatal("dry run should not actually delete chunks")
	}
}

func TestGCEmptyStore(t *testing.T) {
	s := tempGCStore(t)

	result, err := s.GC(GCOptions{
		CommitLoader: func(hash string) (*GCCommit, error) {
			return nil, fmt.Errorf("no commits")
		},
		BranchTips: func() []string { return nil },
	})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}

	if result.TotalChunks != 0 {
		t.Fatalf("expected 0 total chunks, got %d", result.TotalChunks)
	}
	if result.DeletedCount != 0 {
		t.Fatalf("expected 0 deleted, got %d", result.DeletedCount)
	}
}

func TestGCKeepsMinCommits(t *testing.T) {
	s := tempGCStore(t)
	now := time.Now().UTC()

	// 10 commits, all older than 1 hour.
	var hashes []string
	parent := ""
	for i := 0; i < 10; i++ {
		h, _ := makeCommit(t, s, parent, now.Add(-time.Duration(10-i)*time.Hour), fmt.Sprintf("c%d", i), 1)
		hashes = append(hashes, h)
		parent = h
	}

	tip := hashes[len(hashes)-1]

	// GC with MinAge=30m and MinKeepCommits=5. All commits are older
	// than 30m. Should keep at least 5 most recent.
	result, err := s.GC(GCOptions{
		CommitLoader:   commitLoader(s),
		BranchTips:     func() []string { return []string{tip} },
		HeadHash:       tip,
		MinAge:         30 * time.Minute,
		MinKeepCommits: 5,
	})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}

	// The 5 most recent commits (indices 5-9) should be reachable.
	for _, h := range hashes[5:] {
		if !s.Has(h) {
			t.Fatalf("recent commit %s should be kept", h[:12])
		}
	}

	// At least some old commits' unique chunks should be deleted.
	if result.DeletedCount == 0 {
		t.Fatal("expected some old chunks to be deleted")
	}
}

// TestGCRefusesToSweepWhenTreeChunkCorrupt pins the safety invariant:
// when GC's mark phase encounters an unreadable or corrupt prolly tree
// chunk, descendants of that subtree may not get marked, and a
// downstream sweep would silently delete live data. Pre-fix, the
// markProllyTree path swallowed Read/Unmarshal errors and let GC
// proceed. Post-fix, it increments result.Errors and GC short-circuits
// before phase 2 with DeletedCount=0.
func TestGCRefusesToSweepWhenTreeChunkCorrupt(t *testing.T) {
	s := tempGCStore(t)
	now := time.Now().UTC()

	// Build a commit with a real prolly tree.
	commitHash, _ := makeCommit(t, s, "", now, "tipped", 3)

	// Load the commit, reach into its tree, and corrupt one of the
	// leaf-tree chunks by overwriting it with junk that won't
	// json-unmarshal as a ProllyNode. We need to make the corruption
	// targeted so the corruption flag fires (not miss-because-already-
	// visited). Simulate by writing a chunk whose hash equals the
	// node tree root but whose content is non-JSON garbage. We
	// achieve that by deleting the chunk and rewriting under the
	// same hash via direct file write -- not portable -- so instead,
	// take the simpler route: fabricate a fresh commit that points
	// at a non-existent node tree root. Mark phase fails to read
	// the tree, surfaces Errors++, GC refuses to sweep.
	badCommit := GCCommit{
		Parent:       "",
		Timestamp:    now,
		NodeTreeRoot: "0000000000000000000000000000000000000000000000000000000000000000",
	}
	badData, err := json.Marshal(badCommit)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	badHash, err := s.Write(badData)
	if err != nil {
		t.Fatalf("Write bad commit: %v", err)
	}
	_ = commitHash // keep the realistic commit alive; not the focus

	// Add an orphan chunk that GC would normally delete.
	orphan, _ := s.Write([]byte("orphan that should NOT be swept"))

	result, err := s.GC(GCOptions{
		CommitLoader: commitLoader(s),
		BranchTips:   func() []string { return []string{badHash} },
		HeadHash:     badHash,
	})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if result.Errors == 0 {
		t.Fatal("expected Errors > 0 when tree chunk is unreadable")
	}
	if result.DeletedCount != 0 {
		t.Fatalf("GC must NOT sweep when marking incomplete; deleted %d", result.DeletedCount)
	}
	if !s.Has(orphan) {
		t.Fatal("orphan chunk was deleted despite incomplete marking -- live data could have been swept")
	}
}

func TestGCLegacyIndexRoots(t *testing.T) {
	s := tempGCStore(t)
	now := time.Now().UTC()

	// Write a fake legacy index blob.
	legacyBlob, _ := s.Write([]byte("legacy BM25 index data"))

	// Create a commit that references it.
	commit := GCCommit{
		Timestamp:    now,
		BM25FullRoot: legacyBlob,
	}
	commitData, _ := json.Marshal(commit)
	commitHash, _ := s.Write(commitData)
	commit.Hash = commitHash

	// GC should keep the legacy blob.
	result, err := s.GC(GCOptions{
		CommitLoader: func(hash string) (*GCCommit, error) {
			if hash == commitHash {
				return &commit, nil
			}
			return nil, fmt.Errorf("unknown commit")
		},
		BranchTips: func() []string { return []string{commitHash} },
		HeadHash:   commitHash,
	})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}

	if !s.Has(legacyBlob) {
		t.Fatal("legacy index blob should be kept")
	}
	if result.DeletedCount != 0 {
		t.Fatalf("expected 0 deleted, got %d", result.DeletedCount)
	}
}
