package cacheproxy

import (
	"context"
	"database/sql"
	"strconv"
	"time"
)

// A completed positive-byte playback GET is observable server evidence, not a
// decoded first frame. Keep this separate from demand-based retention scoring.
func recordPlaybackTransfer(ctx context.Context, tx *sql.Tx, e usageEvent) error {
	if e.source != "http" || e.method != "GET" || e.outcome != "completed" || e.bytes <= 0 || (e.status != 200 && e.status != 206) || e.songID == "" {
		return nil
	}
	id, err := strconv.ParseInt(e.songID, 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO songs(song_id) VALUES (?) ON CONFLICT DO NOTHING", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO song_playback(song_id,transfer_count,last_transfer_at) VALUES (?,1,?)
 ON CONFLICT(song_id) DO UPDATE SET transfer_count=transfer_count+1,last_transfer_at=excluded.last_transfer_at
 WHERE excluded.last_transfer_at-song_playback.last_transfer_at>=?`, id, e.at, demandWindow.Milliseconds()); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO playback_latency(cache_result,samples,first_body_ns) VALUES (?,1,?)
 ON CONFLICT(cache_result) DO UPDATE SET samples=samples+1,first_body_ns=first_body_ns+excluded.first_body_ns`, e.cache, e.firstBodyNS)
	return err
}

func addPlaybackStats(db *sql.DB, v *TrafficStats) {
	var exists bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='song_playback')").Scan(&exists); err != nil || !exists {
		return
	}
	if err := db.QueryRow("SELECT COALESCE(SUM(transfer_count),0) FROM song_playback").Scan(&v.PlaybackTransfers); err != nil {
		v.Error = err.Error()
		return
	}
	rows, err := db.Query("SELECT cache_result,samples,first_body_ns FROM playback_latency")
	if err != nil {
		v.Error = err.Error()
		return
	}
	defer rows.Close()
	for rows.Next() {
		var cache string
		var count uint64
		var total int64
		if err := rows.Scan(&cache, &count, &total); err != nil {
			v.Error = err.Error()
			return
		}
		if count == 0 {
			continue
		}
		ms := float64(total) / float64(time.Millisecond) / float64(count)
		if cache == "HIT" {
			v.LocalBodyMS, v.LocalBodySamples = &ms, count
		}
		if cache == "MISS" {
			v.ColdBodyMS, v.ColdBodySamples = &ms, count
		}
	}
	if err := rows.Err(); err != nil {
		v.Error = err.Error()
	}
}
