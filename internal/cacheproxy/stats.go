package cacheproxy

import (
	"database/sql"
	"sync"
)

// TrafficStats preserves lifetime traffic totals and playback first-body samples.
type TrafficStats struct {
	PlaybackTransfers uint64   `json:"playbackTransfers"`
	LocalBodyMS       *float64 `json:"localBodyMS"`
	ColdBodyMS        *float64 `json:"coldBodyMS"`
	LocalBodySamples  uint64   `json:"localBodySamples"`
	ColdBodySamples   uint64   `json:"coldBodySamples"`
	SavedBytes        int64    `json:"savedBytes"`
	Requests          uint64   `json:"requests"`
	Hits              uint64   `json:"hits"`
	Misses            uint64   `json:"misses"`
	HitRate           *float64 `json:"hitRate"`
	Error             string   `json:"error,omitempty"`
}

type trafficStats struct {
	savedBytes   int64
	mu           sync.Mutex
	hits, misses uint64
	err          string
}

func (s *Server) recordTraffic(method, cache, outcome string, status int, bytes int64) {
	if method != "GET" || outcome != "completed" || (status != 200 && status != 206) {
		return
	}
	s.stats.mu.Lock()
	defer s.stats.mu.Unlock()
	switch cache {
	case "HIT":
		s.stats.savedBytes += bytes
		s.stats.hits++
	case "MISS":
		s.stats.misses++
	default:
		return
	}
	s.persistTraffic()
}

func (s *Server) TrafficStats() TrafficStats {
	s.stats.mu.Lock()
	defer s.stats.mu.Unlock()
	v := TrafficStats{SavedBytes: s.stats.savedBytes, Hits: s.stats.hits, Misses: s.stats.misses, Requests: s.stats.hits + s.stats.misses,
		Error: s.stats.err}
	if v.Requests > 0 {
		rate := 100 * float64(v.Hits) / float64(v.Requests)
		v.HitRate = &rate
	}
	if s.usage != nil {
		addPlaybackStats(s.usage.db, &v)
	}
	return v
}

const trafficSchema = `CREATE TABLE IF NOT EXISTS traffic_totals (
 id INTEGER PRIMARY KEY CHECK(id=1), saved_bytes INTEGER NOT NULL,
 hits INTEGER NOT NULL, misses INTEGER NOT NULL, local_samples INTEGER NOT NULL,
 local_ns INTEGER NOT NULL, upstream_samples INTEGER NOT NULL, upstream_ns INTEGER NOT NULL
);`

func (s *Server) loadTraffic(db *sql.DB) error {
	return db.QueryRow(`SELECT saved_bytes,hits,misses FROM traffic_totals WHERE id=1`).Scan(
		&s.stats.savedBytes, &s.stats.hits, &s.stats.misses)
}

// Called under stats.mu after each observation, so a later snapshot cannot be
// overwritten by an earlier one. Counts survive request-detail retention.
func (s *Server) persistTraffic() {
	if s.usage == nil {
		return
	}
	_, err := s.usage.db.Exec(`UPDATE traffic_totals SET saved_bytes=?,hits=?,misses=? WHERE id=1`,
		s.stats.savedBytes, s.stats.hits, s.stats.misses)
	s.stats.err = ""
	if err != nil {
		s.stats.err = "播放统计保存失败：" + err.Error()
		s.cfg.Logger.Error("traffic_save_failed", "error", err)
	}
}

// ReadTrafficStats reads without creating a database or starting a cache engine.
func ReadTrafficStats(root string) TrafficStats {
	s := &Server{}
	db, err := readStorageDatabase(root)
	if err != nil {
		return TrafficStats{Error: err.Error()}
	}
	if db == nil {
		return s.TrafficStats()
	}
	defer db.Close()
	s.usage = &usageStore{db: db}
	err = s.loadTraffic(db)
	if err != nil {
		s.stats.err = "播放统计读取失败：" + err.Error()
	}
	return s.TrafficStats()
}
