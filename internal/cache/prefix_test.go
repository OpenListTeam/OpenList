package cache

import (
	"testing"
	"time"
)

func TestKeyedCacheDeletePrefix(t *testing.T) {
	c := NewKeyedCache[int](time.Hour)
	c.Set("/foo", 1)
	c.Set("/foo/bar", 2)
	c.Set("/foobar", 3)

	c.DeletePrefix("/foo")

	if _, ok := c.Get("/foo"); ok {
		t.Fatal("DeletePrefix() kept the exact key")
	}
	if _, ok := c.Get("/foo/bar"); ok {
		t.Fatal("DeletePrefix() kept a descendant key")
	}
	if value, ok := c.Get("/foobar"); !ok || value != 3 {
		t.Fatalf("DeletePrefix() removed a sibling key: value=%d, ok=%v", value, ok)
	}
}

func TestKeyedCacheDeletePrefixRoot(t *testing.T) {
	c := NewKeyedCache[int](time.Hour)
	c.Set("/", 1)
	c.Set("/foo", 2)

	c.DeletePrefix("/")

	if _, ok := c.Get("/"); ok {
		t.Fatal("DeletePrefix(/) kept the root key")
	}
	if _, ok := c.Get("/foo"); ok {
		t.Fatal("DeletePrefix(/) kept a descendant key")
	}
}

func TestTypedCacheDeleteKeyPrefix(t *testing.T) {
	c := NewTypedCache[int](time.Hour)
	c.SetType("/foo", "link", 1)
	c.SetType("/foo/bar", "link", 2)
	c.SetType("/foobar", "link", 3)

	c.DeleteKeyPrefix("/foo")

	if _, ok := c.GetType("/foo", "link"); ok {
		t.Fatal("DeleteKeyPrefix() kept the exact key")
	}
	if _, ok := c.GetType("/foo/bar", "link"); ok {
		t.Fatal("DeleteKeyPrefix() kept a descendant key")
	}
	if value, ok := c.GetType("/foobar", "link"); !ok || value != 3 {
		t.Fatalf("DeleteKeyPrefix() removed a sibling key: value=%d, ok=%v", value, ok)
	}
}
