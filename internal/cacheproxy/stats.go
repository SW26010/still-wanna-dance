package cacheproxy

import (
	"sync"
	"time"
)

// TrafficStats covers this engine's lifetime. Latencies measure response headers,
// not transfer throughput or the player's time to decoded playback.
type TrafficStats struct {
	SavedBytes       int64    `json:"savedBytes"`
	Requests         uint64   `json:"requests"`
	Hits             uint64   `json:"hits"`
	Misses           uint64   `json:"misses"`
	HitRate          *float64 `json:"hitRate"`
	UpstreamSamples  uint64   `json:"upstreamSamples"`
	LocalSamples     uint64   `json:"localSamples"`
	UpstreamMS       *float64 `json:"upstreamMS"`
	LocalMS          *float64 `json:"localMS"`
	ReductionPercent *float64 `json:"reductionPercent"`
}

type trafficStats struct {
	savedBytes                    int64
	mu                            sync.Mutex
	hits, misses, upstreamSamples uint64
	local, upstream               time.Duration
}

func (s *Server) recordTraffic(method, cache, outcome string, status int, latency time.Duration, bytes int64) {
	if method != "GET" || outcome != "completed" || (status != 200 && status != 206) {
		return
	}
	s.stats.mu.Lock()
	defer s.stats.mu.Unlock()
	switch cache {
	case "HIT":
		s.stats.savedBytes += bytes
		s.stats.hits++
		s.stats.local += latency
	case "MISS":
		s.stats.misses++
	}
}

func (s *Server) recordUpstream(latency time.Duration) {
	s.stats.mu.Lock()
	defer s.stats.mu.Unlock()
	s.stats.upstreamSamples++
	s.stats.upstream += latency
}

func (s *Server) TrafficStats() TrafficStats {
	s.stats.mu.Lock()
	defer s.stats.mu.Unlock()
	v := TrafficStats{SavedBytes: s.stats.savedBytes, Hits: s.stats.hits, Misses: s.stats.misses, Requests: s.stats.hits + s.stats.misses,
		LocalSamples: s.stats.hits, UpstreamSamples: s.stats.upstreamSamples}
	if v.Requests > 0 {
		rate := 100 * float64(v.Hits) / float64(v.Requests)
		v.HitRate = &rate
	}
	if v.LocalSamples > 0 {
		ms := float64(s.stats.local) / float64(time.Millisecond) / float64(v.LocalSamples)
		v.LocalMS = &ms
	}
	if v.UpstreamSamples > 0 {
		ms := float64(s.stats.upstream) / float64(time.Millisecond) / float64(v.UpstreamSamples)
		v.UpstreamMS = &ms
	}
	if v.LocalMS != nil && v.UpstreamMS != nil && *v.UpstreamMS > 0 {
		reduction := 100 * (1 - *v.LocalMS / *v.UpstreamMS)
		v.ReductionPercent = &reduction
	}
	return v
}
