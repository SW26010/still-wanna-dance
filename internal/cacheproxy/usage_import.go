package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrSongUsageExists = errors.New("已有歌曲需求统计；请重新预览并明确确认整表覆盖")

type songUsage struct {
	count, last int64
	score       float64
}

// countedDemand is shared by live GETs and explicit historical initialization.
// A skipped event never advances the anchor.
func countedDemand(score float64, last, at int64, exists bool) (float64, bool) {
	if exists && at-last < demandWindow.Milliseconds() {
		return score, false
	}
	if !exists {
		return 1, true
	}
	return retentionScore(score, last, at) + 1, true
}

// SongUsageImport holds aggregates only; no private event history is retained.
// Observe must be called in chronological order after source validation.
type SongUsageImport struct {
	rows       map[int64]songUsage
	Events     int   `json:"events"`
	Duplicates int   `json:"duplicates"`
	FirstAt    int64 `json:"firstAt"`
	LastAt     int64 `json:"lastAt"`
}

func (h *SongUsageImport) Songs() int { return len(h.rows) }

func (h *SongUsageImport) Observe(id, at int64) {
	if id <= 0 || at <= 0 {
		return
	}
	if h.rows == nil {
		h.rows = make(map[int64]songUsage)
	}
	row, exists := h.rows[id]
	score, counted := countedDemand(row.score, row.last, at, exists)
	if !counted {
		h.Duplicates++
		return
	}
	h.rows[id] = songUsage{row.count + 1, at, score}
	h.Events++
	if h.FirstAt == 0 || at < h.FirstAt {
		h.FirstAt = at
	}
	h.LastAt = max(h.LastAt, at)
}

func (s *Store) SongUsageCount(ctx context.Context) (resultValue int, resultErr error) {
	if !s.beginRequest() {
		return 0, context.Canceled
	}
	defer s.wg.Done()
	defer func() { resultErr = s.storageError(resultErr) }()
	s.usage.flush()
	var count int
	err := s.usage.db.QueryRowContext(ctx, "SELECT count(*) FROM song_usage").Scan(&count)
	return count, err
}

// ImportSongUsage serializes against the existing observation queue. All
// accepted GETs are drained before the in-transaction empty check; later GETs
// enqueue after commit. Replacement and any placeholder songs commit together.
func (s *Store) ImportSongUsage(ctx context.Context, h *SongUsageImport, overwrite bool) error {
	if h == nil || h.Events == 0 || len(h.rows) == 0 {
		return errors.New("没有有效历史需求，原统计保持不变")
	}
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u := s.usage
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return errors.New("存储已关闭，请重新打开应用")
	}
	if !u.started {
		return s.storageError(replaceSongUsage(ctx, u.db, h, overwrite))
	}
	done := make(chan struct{})
	select {
	case u.events <- usageEvent{barrier: done}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.storageError(replaceSongUsage(ctx, u.db, h, overwrite))
}

func replaceSongUsage(ctx context.Context, db *sql.DB, h *SongUsageImport, overwrite bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM song_usage)").Scan(&exists); err != nil {
		return err
	}
	if exists && !overwrite {
		return ErrSongUsageExists
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM song_usage"); err != nil {
		return err
	}
	songs, err := tx.PrepareContext(ctx, "INSERT INTO songs(id) VALUES (?) ON CONFLICT DO NOTHING")
	if err != nil {
		return err
	}
	defer songs.Close()
	usage, err := tx.PrepareContext(ctx, "INSERT INTO song_usage(song_id,demand_count,demand_score,last_demand_at) VALUES (?,?,?,?)")
	if err != nil {
		return err
	}
	defer usage.Close()
	for id, row := range h.rows {
		if _, err = songs.ExecContext(ctx, id); err != nil {
			return err
		}
		if _, err = usage.ExecContext(ctx, id, row.count, row.score, row.last); err != nil {
			return err
		}
	}
	return tx.Commit()
}
