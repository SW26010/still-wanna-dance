package cacheproxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"
)

type queueMetadata struct {
	timing mediaTiming
	title  string
}

func (s *Server) queueTimings(ctx context.Context, ids []int64) (map[string]queueMetadata, error) {
	result := make(map[string]queueMetadata)
	if s.usage == nil || len(ids) == 0 {
		return result, nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := s.usage.db.QueryContext(ctx, `SELECT q.value,m.byte_size,s.end,substr(COALESCE(NULLIF(s.name,''),s.composed_title,''),1,300)
 FROM json_each(?) q LEFT JOIN songs s ON s.song_id=q.value
 LEFT JOIN song_media sm ON sm.song_id=s.song_id LEFT JOIN media m ON m.md5=sm.md5`, string(encoded))
	if err != nil {
		return nil, s.storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var size sql.NullInt64
		var seconds sql.NullFloat64
		var title string
		if err := rows.Scan(&id, &size, &seconds, &title); err != nil {
			return nil, s.storageError(err)
		}
		if id > 0 {
			result[strconv.FormatInt(id, 10)] = queueMetadata{estimateMediaTiming(size.Int64, seconds.Float64), title}
		}
	}
	return result, s.storageError(rows.Err())
}

// Names for presentation come from the same local catalog snapshot as timing.
// The log supplies only IDs/order. Return a copy, never mutable source objects.
func (s *Server) QueueTitles() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	titles := make(map[string]string, len(s.prefetchSources))
	for id, q := range s.prefetchSources {
		if q.title != "" {
			titles[id] = q.title
		}
	}
	return titles
}

// Call under Server.mu. Request completion does not mean playback completion:
// when the last GET leaves, retain the last calculated queue start. This is a
// single scheduling anchor, not a registry of historical requests or demands.
func (s *Server) retimeQueueLocked(now time.Time) {
	var earliest time.Time
	for d := range s.demands {
		if d.clock == nil || !d.exit.active(d.ctx) {
			continue
		}
		end := d.clock.end()
		if earliest.IsZero() || end.Before(earliest) {
			earliest = end
		}
	}
	if !earliest.IsZero() {
		s.queueStart = earliest
	}
	if s.queueStart.IsZero() {
		if len(s.prefetchOrder) == 0 {
			return
		}
		s.queueStart = now
	}
	start := s.queueStart
	for i, id := range s.prefetchOrder {
		duration := typicalDuration
		if q := s.prefetchSources[strconv.FormatInt(id, 10)]; q != nil {
			duration = q.timing.duration
			if q.position == i {
				if q.startAt != start {
					q.expired = false // Only an actual source clock change permits another preparation.
				}
				if q.startAt != start && s.cfg.Logger != nil {
					s.cfg.Logger.Debug("queue_playback_timing", "song_id", q.song, "position", i, "starts_at", start, "media_bytes", q.timing.size, "duration_seconds", q.timing.duration.Seconds())
				}
				q.startAt = start
			}
		}
		start = start.Add(duration)
	}
}

// URL resolution can fill a previously unknown size before the next log
// snapshot. Only the still-current queue identity may receive that update.
func (s *Server) refreshQueueResourceTiming(ctx context.Context, v video) error {
	q, _ := ctx.Value(queueDemandKey{}).(*queueSource)
	if q == nil {
		return nil
	}
	timing, err := s.mediaTiming(ctx, v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() == nil && s.prefetchSources[q.song] == q && q.timing != timing {
		q.expired = false
		q.timing = timing
		s.retimeQueueLocked(time.Now())
		s.notifyCapacityLocked()
	}
	return nil
}
