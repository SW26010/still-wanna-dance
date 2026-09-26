package cacheproxy

import (
	"testing"
	"time"
)

func TestRouteLRUEviction(t *testing.T) {
	var cache routeLRU[string]
	now := time.Now()
	cache.put("a", "first", time.Time{}, 2)
	cache.put("b", "second", time.Time{}, 2)
	if value, ok := cache.get("a", now); !ok || value != "first" {
		t.Fatalf("cache hit = %q, %v", value, ok)
	}
	cache.put("c", "third", time.Time{}, 2)
	if _, ok := cache.get("b", now); ok {
		t.Fatal("least recently used entry survived eviction")
	}
	cache.put("a", "updated", time.Time{}, 2)
	cache.put("d", "fourth", time.Time{}, 2)
	if value, ok := cache.get("a", now); !ok || value != "updated" {
		t.Fatalf("updated entry = %q, %v", value, ok)
	}
	if _, ok := cache.get("c", now); ok {
		t.Fatal("updating an entry did not refresh recency")
	}
	if len(cache.items) != 2 || cache.order.Len() != 2 {
		t.Fatal("cache exceeded capacity or retained stale list entries")
	}
}

func TestRouteLRUExpiration(t *testing.T) {
	var cache routeLRU[string]
	now := time.Now()
	cache.put("a", "old", now.Add(time.Minute), 2)
	if _, ok := cache.get("a", now); !ok {
		t.Fatal("entry expired early")
	}
	if _, ok := cache.get("a", now.Add(time.Minute)); ok {
		t.Fatal("expired entry returned")
	}
	if len(cache.items) != 0 || cache.order.Len() != 0 {
		t.Fatal("expired entry was not removed")
	}
	cache.put("a", "new", now.Add(2*time.Minute), 2)
	if value, ok := cache.get("a", now.Add(time.Minute)); !ok || value != "new" {
		t.Fatalf("refreshed entry = %q, %v", value, ok)
	}
}
