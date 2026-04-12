package index

import (
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func newTestCollCache(t *testing.T) *BboltCollectionCache {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "coll.db"), 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c, err := NewBboltCollectionCache(db)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCollCacheAddAndMembers(t *testing.T) {
	c := newTestCollCache(t)
	c.AddMember("coll1", "item1")
	c.AddMember("coll1", "item2")
	c.AddMember("coll1", "item3")

	ids := c.Members("coll1")
	if len(ids) != 3 {
		t.Fatalf("expected 3 members, got %d", len(ids))
	}
}

func TestCollCacheDedup(t *testing.T) {
	c := newTestCollCache(t)
	c.AddMember("coll1", "item1")
	c.AddMember("coll1", "item1") // duplicate

	ids := c.Members("coll1")
	if len(ids) != 1 {
		t.Fatalf("expected 1 member (dedup), got %d", len(ids))
	}
}

func TestCollCacheRemove(t *testing.T) {
	c := newTestCollCache(t)
	c.AddMember("coll1", "item1")
	c.AddMember("coll1", "item2")
	c.RemoveMember("coll1", "item1")

	ids := c.Members("coll1")
	if len(ids) != 1 || ids[0] != "item2" {
		t.Fatalf("expected [item2], got %v", ids)
	}
}

func TestCollCacheRemoveLastItem(t *testing.T) {
	c := newTestCollCache(t)
	c.AddMember("coll1", "only")
	c.RemoveMember("coll1", "only")

	ids := c.Members("coll1")
	if len(ids) != 0 {
		t.Fatalf("expected empty after removing last, got %v", ids)
	}
}

func TestCollCacheMemberCount(t *testing.T) {
	c := newTestCollCache(t)
	c.AddMember("coll1", "a")
	c.AddMember("coll1", "b")
	c.AddMember("coll1", "c")

	if cnt := c.MemberCount("coll1"); cnt != 3 {
		t.Fatalf("expected 3, got %d", cnt)
	}
	if cnt := c.MemberCount("nonexistent"); cnt != 0 {
		t.Fatalf("expected 0 for nonexistent, got %d", cnt)
	}
}

func TestCollCacheDeleteCollection(t *testing.T) {
	c := newTestCollCache(t)
	c.AddMember("coll1", "item1")
	c.DeleteCollection("coll1")

	ids := c.Members("coll1")
	if len(ids) != 0 {
		t.Fatalf("expected empty after delete, got %v", ids)
	}
}

func TestCollCacheMultipleCollections(t *testing.T) {
	c := newTestCollCache(t)
	c.AddMember("c1", "a")
	c.AddMember("c1", "b")
	c.AddMember("c2", "x")

	if len(c.Members("c1")) != 2 {
		t.Fatal("c1 should have 2 members")
	}
	if len(c.Members("c2")) != 1 {
		t.Fatal("c2 should have 1 member")
	}
}

func TestCollCacheEmptyCollection(t *testing.T) {
	c := newTestCollCache(t)
	ids := c.Members("empty")
	if ids != nil {
		t.Fatalf("expected nil for empty collection, got %v", ids)
	}
}
