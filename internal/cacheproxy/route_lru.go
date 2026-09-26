package cacheproxy

import (
	"container/list"
	"time"
)

const (
	routeCacheLimit = 1024
	routeSongsLimit = 4096
)

// routeLRU is bounded even when expired entries are never read again.
// Callers must hold routeMu. The zero value is ready for use.
type routeLRU[T any] struct {
	items map[string]*list.Element
	order list.List
}

type routeLRUEntry[T any] struct {
	key   string
	value T
	until time.Time // zero means no expiration
}

func (c *routeLRU[T]) get(key string, now time.Time) (T, bool) {
	if elem := c.items[key]; elem != nil {
		entry := elem.Value.(routeLRUEntry[T])
		if entry.until.IsZero() || now.Before(entry.until) {
			c.order.MoveToFront(elem)
			return entry.value, true
		}
		c.remove(elem)
	}
	var zero T
	return zero, false
}

func (c *routeLRU[T]) put(key string, value T, until time.Time, limit int) {
	entry := routeLRUEntry[T]{key: key, value: value, until: until}
	if elem := c.items[key]; elem != nil {
		elem.Value = entry
		c.order.MoveToFront(elem)
		return
	}
	if c.items == nil {
		c.items = make(map[string]*list.Element)
	}
	c.items[key] = c.order.PushFront(entry)
	for len(c.items) > limit {
		c.remove(c.order.Back())
	}
}

func (c *routeLRU[T]) remove(elem *list.Element) {
	delete(c.items, elem.Value.(routeLRUEntry[T]).key)
	c.order.Remove(elem)
}
