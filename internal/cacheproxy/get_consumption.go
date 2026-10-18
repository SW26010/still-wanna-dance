package cacheproxy

import (
	"fmt"
	"net/http"
	"time"
)

// Only the HTTP response writer advances delivery, never the Reader or Seek.
// A single contiguous response needs constant-size progress state. Source and
// playback clock identities survive block consumption until the request exits.
type getConsumption struct {
	s                                *Server
	demand                           *downloadDemand
	size, position, limit, nextBlock int64
	active                           bool
	err                              error
}

func (g *getConsumption) start(status int, header http.Header) {
	start, end := int64(0), g.size
	switch status {
	case http.StatusOK:
	case http.StatusPartialContent:
		var last, size int64
		if n, err := fmt.Sscanf(header.Get("Content-Range"), "bytes %d-%d/%d", &start, &last, &size); err != nil || n != 3 || size != g.size || start < 0 || last < start || last >= size {
			return
		}
		end = last + 1
	default:
		return
	}
	first, last := blocksForRange(start, end)
	g.position, g.limit, g.nextBlock, g.active = start, end, first, true
	g.s.mu.Lock()
	d := g.demand
	if d.firstBlock != g.nextBlock || d.lastBlock != last {
		if !g.s.demands[d] {
			g.s.mu.Unlock()
			return
		}
		if err := g.s.replaceUseLocked(d.useSpan(), useSpan{d.key, g.nextBlock, last}, true); err != nil {
			g.err, g.active = err, false
			g.s.mu.Unlock()
			return
		}
		d.firstBlock, d.lastBlock = g.nextBlock, last
		g.s.notifyCapacityLocked()
	}
	g.s.mu.Unlock()
}

func (g *getConsumption) accept(n int64) {
	if !g.active {
		return
	}
	g.position += min(n, g.limit-g.position)
	next := g.position / rangeBlockSize
	if g.position == g.limit {
		next = blockCount(g.limit)
	}
	if next == g.nextBlock {
		return
	}
	g.nextBlock = next
	g.s.mu.Lock()
	// The request may have been canceled while Write was returning. Its
	// withdrawal already transferred/released the entire remaining span.
	if !g.s.demands[g.demand] {
		g.s.mu.Unlock()
		return
	}
	g.s.releaseUseLocked(useSpan{g.demand.key, g.demand.firstBlock, next}, time.Now())
	g.demand.firstBlock = next
	g.s.notifyCapacityLocked()
	g.s.mu.Unlock()
}
