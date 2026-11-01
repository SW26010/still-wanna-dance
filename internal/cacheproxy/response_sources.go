package cacheproxy

import (
	"context"
	"time"
)

// ResponseSources holds frozen, block-weighted bytes, not HTTP transfer bytes.
// A block is counted once per GET when any of its requested bytes is accepted
// by the response writer. The final block contributes only its actual size.
type ResponseSources struct {
	DownloadBytes int64 `json:"downloadBytes"`
	CacheBytes    int64 `json:"cacheBytes"`
	LocalBytes    int64 `json:"localBytes"`
}

// readSources belongs to one local reader. Waiting is a reader observation,
// never a shared block property or a copy of the scheduler's Blocking state.
// ServeContent reads and writes one contiguous range synchronously, so only
// the latest waited block is needed; no per-block map or locking is required.
type readSources struct {
	size, blockSize int64
	waitedBlock     int64 // One-based; zero means no missing-block wait yet.
	local           bool
}

type responseSourceCounter struct {
	reader    *readSources
	nextBlock int64
	totals    ResponseSources
}

func (c *responseSourceCounter) accept(start, end int64) {
	if end <= start {
		return
	}
	r := c.reader
	first := max(start/r.blockSize, c.nextBlock)
	last := blockCount(end, r.blockSize)
	if first >= last {
		return
	}
	// Count an arbitrarily large write in constant time, including a short tail.
	limit := r.size
	if last < blockCount(r.size, r.blockSize) {
		limit = last * r.blockSize
	}
	bytes := limit - first*r.blockSize
	if r.local {
		c.totals.LocalBytes += bytes
	} else {
		var downloaded int64
		if r.waitedBlock > first && r.waitedBlock <= last {
			downloaded = blockBytes(r.size, r.waitedBlock-1, r.blockSize)
		}
		c.totals.DownloadBytes += downloaded
		c.totals.CacheBytes += bytes - downloaded
	}
	c.nextBlock = last
}

// ReadResponseSources sums frozen contributions from all retained requests,
// independently of history pagination and time filters.
func ReadResponseSources(ctx context.Context, databasePath string) (ResponseSources, error) {
	var result ResponseSources
	db, err := readDatabaseFile(databasePath)
	if err != nil {
		return result, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err = db.QueryRowContext(ctx, "SELECT COALESCE(SUM(download_bytes),0),COALESCE(SUM(cache_bytes),0),COALESCE(SUM(local_bytes),0) FROM request_events").Scan(&result.DownloadBytes, &result.CacheBytes, &result.LocalBytes)
	return result, err
}
