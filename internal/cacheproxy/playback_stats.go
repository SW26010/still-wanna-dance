package cacheproxy

import (
	"context"
	"database/sql"
)

// A completed positive-byte playback GET is observable server evidence, not a
// decoded first frame. Keep this separate from demand-based retention scoring.
// Deduplication uses serialized completion observations, not request starts.
func recordPlaybackTransfer(ctx context.Context, tx *sql.Tx, e usageEvent, id int64) error {
	if e.method != "GET" || e.outcome != "completed" || e.bytes <= 0 || (e.status != 200 && e.status != 206) || id == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO songs(id) VALUES (?) ON CONFLICT DO NOTHING", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO song_playback(song_id,transfer_count,last_transfer_at) VALUES (?,1,?)
 ON CONFLICT(song_id) DO UPDATE SET transfer_count=transfer_count+1,last_transfer_at=excluded.last_transfer_at
 WHERE excluded.last_transfer_at-song_playback.last_transfer_at>=?`, id, e.completedAt, demandWindow.Milliseconds()); err != nil {
		return err
	}
	return nil
}

func addPlaybackStats(db *sql.DB, v *TrafficStats) {
	if err := db.QueryRow("SELECT COALESCE(SUM(transfer_count),0) FROM song_playback").Scan(&v.PlaybackTransfers); err != nil {
		v.Error = err.Error()
		return
	}
}
