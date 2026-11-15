package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"
)

// Each snapshot keeps one successful value per operation (one last filter for
// counts/windows), not an unbounded cache per request. Concurrent callers share
// work; cancellation or failure never becomes a cached result.
type historyCache[T any] struct {
	mu       sync.Mutex
	value    T
	valid    bool
	building *historyBuild
}

func (c *historyCache[T]) get(ctx context.Context, accepts func(T) bool, read func() (T, error)) (T, error) {
	var zero T
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		c.mu.Lock()
		if c.valid && accepts(c.value) {
			value := c.value
			c.mu.Unlock()
			return value, nil
		}
		if b := c.building; b != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-b.done:
				if b.err != nil && !errors.Is(b.err, context.Canceled) && !errors.Is(b.err, context.DeadlineExceeded) {
					return zero, b.err
				}
			}
			continue
		}
		b := &historyBuild{done: make(chan struct{})}
		c.building = b
		c.mu.Unlock()
		value, err := read()
		if err == nil {
			err = ctx.Err()
		}
		c.mu.Lock()
		if err == nil {
			c.value, c.valid = value, true
		}
		b.err, c.building = err, nil
		close(b.done)
		c.mu.Unlock()
		return value, err
	}
}

type historyCountKey struct {
	from, to             int64
	hideAdjacentCanceled bool
}
type historyCount struct {
	key   historyCountKey
	value RequestHistoryCount
}

func (s *historySnapshot) prepareIndex(ctx context.Context, cache *historyCache[bool], ddl string) error {
	_, err := cache.get(ctx, func(bool) bool { return true }, func() (bool, error) {
		select {
		case s.indexGate <- struct{}{}:
			defer func() { <-s.indexGate }()
		case <-ctx.Done():
			return false, ctx.Err()
		}
		writer, err := sql.Open("sqlite", storageDatabaseURI(s.path, "rw"))
		if err != nil {
			return false, err
		}
		defer writer.Close()
		_, err = writer.ExecContext(ctx, ddl)
		return err == nil, err
	})
	return err
}

// Names are current catalog data, not immutable request facts. Resolving only
// this bounded page on the live DB avoids rebuilding history for catalog edits.
func refreshHistorySongNames(ctx context.Context, path string, page *RecentRequests) error {
	var args []any
	for _, r := range page.Requests {
		if r.SongID != "" {
			args = append(args, r.SongID)
		}
	}
	if len(args) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	db, err := readDatabaseFile(path)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT CAST(id AS TEXT),COALESCE(name,'') FROM songs WHERE id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+")", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	names := make(map[string]string, len(args))
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		names[id] = name
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range page.Requests {
		page.Requests[i].SongTitle = names[page.Requests[i].SongID]
	}
	return nil
}
