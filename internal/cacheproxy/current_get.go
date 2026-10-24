package cacheproxy

import (
	"sort"
	"sync/atomic"
	"time"
)

type CurrentGET struct {
	ID            uint64 `json:"id"`
	At            int64  `json:"at"`
	SongID        string `json:"songID"`
	Resource      string `json:"resource"`
	Range         string `json:"range"`
	Status        int    `json:"status"`
	Bytes         int64  `json:"bytes"`
	ResponseBytes *int64 `json:"responseBytes"`
}
type CurrentGETs struct {
	Requests []CurrentGET `json:"requests"`
	Total    int          `json:"total"`
}
type getIdentity struct{ songID, resource string }
type getResponse struct {
	status   int
	expected *int64
}
type currentGET struct {
	id          uint64
	at          int64
	rangeHeader string
	identity    atomic.Pointer[getIdentity]
	response    atomic.Pointer[getResponse]
	bytes       atomic.Int64
}

func (g *currentGET) identify(songID, resource string) {
	if g != nil {
		g.identity.Store(&getIdentity{songID, resource})
	}
}
func (s *Server) beginGET(id uint64, at time.Time, rangeHeader string) (*currentGET, func()) {
	g := &currentGET{id: id, at: at.UnixMilli(), rangeHeader: rangeHeader[:min(300, len(rangeHeader))]}
	s.getMu.Lock()
	if s.currentGETs == nil {
		s.currentGETs = map[uint64]*currentGET{}
	}
	s.currentGETs[id] = g
	s.getMu.Unlock()
	return g, func() { s.getMu.Lock(); delete(s.currentGETs, id); s.getMu.Unlock() }
}

// CurrentGETs observes incoming handlers only, separately from shared upstream
// flights. No database/network/file access, no demand or lifecycle ownership.
// Successful writes add one atomic count; snapshots expose at most 50 handlers.
func (s *Server) CurrentGETs() CurrentGETs {
	result := CurrentGETs{Requests: []CurrentGET{}}
	s.getMu.Lock()
	items := make([]*currentGET, 0, len(s.currentGETs))
	for _, g := range s.currentGETs {
		items = append(items, g)
	}
	s.getMu.Unlock()
	result.Total = len(items)
	sort.Slice(items, func(i, j int) bool { return items[i].id < items[j].id })
	for _, g := range items[:min(50, len(items))] {
		r := CurrentGET{ID: g.id, At: g.at, Range: g.rangeHeader, Bytes: g.bytes.Load()}
		if identity := g.identity.Load(); identity != nil {
			r.SongID, r.Resource = identity.songID, identity.resource
		}
		if response := g.response.Load(); response != nil {
			r.Status, r.ResponseBytes = response.status, response.expected
		}
		result.Requests = append(result.Requests, r)
	}
	return result
}
