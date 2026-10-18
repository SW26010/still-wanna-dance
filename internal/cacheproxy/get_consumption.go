package cacheproxy

import (
	"fmt"
	"net/http"
)

// Only the HTTP response writer advances delivery, never the Reader or Seek.
// A single contiguous response needs constant-size progress state. Source and
// playback clock identities survive block consumption until the request exits.
type getConsumption struct {
	s                                *Server
	demand                           *downloadDemand
	size, position, limit, nextBlock int64
	active                           bool
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
	g.position, g.limit, g.nextBlock, g.active = start, end, start/rangeBlockSize, true
	g.s.mu.Lock()
	d := g.demand
	last := (end + rangeBlockSize - 1) / rangeBlockSize
	if d.firstBlock != g.nextBlock || d.lastBlock != last {
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
		next = (g.limit + rangeBlockSize - 1) / rangeBlockSize
	}
	if next == g.nextBlock {
		return
	}
	g.nextBlock = next
	g.s.mu.Lock()
	g.demand.firstBlock = next
	g.s.notifyCapacityLocked()
	g.s.mu.Unlock()
}
