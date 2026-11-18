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

func (s *Store) queueTimings(ctx context.Context, ids []int64) (resultValue map[string]queueMetadata, resultErr error) {
	defer func() { resultErr = s.storageError(resultErr) }()
	result := make(map[string]queueMetadata)
	if s == nil || s.usage == nil || len(ids) == 0 {
		return result, nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := s.usage.db.QueryContext(ctx, `SELECT q.value,m.byte_size,s.end,substr(COALESCE(s.name,''),1,300)
 FROM json_each(?) q LEFT JOIN songs s ON s.id=q.value
 LEFT JOIN song_media sm ON sm.song_id=s.id LEFT JOIN media m ON m.md5=sm.md5`, string(encoded))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var size sql.NullInt64
		var seconds sql.NullFloat64
		var title string
		if err := rows.Scan(&id, &size, &seconds, &title); err != nil {
			return nil, err
		}
		if id > 0 {
			result[strconv.FormatInt(id, 10)] = queueMetadata{estimateMediaTiming(size.Int64, seconds.Float64), title}
		}
	}
	return result, rows.Err()
}

func (s *Server) queueTimings(ctx context.Context, ids []int64) (map[string]queueMetadata, error) {
	return s.Store.queueTimings(ctx, ids)
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

// QueueNeedsPreparation also covers a completed member whose automatic refetch
// expired. Only a real clock/metadata change rearms that member; readable blocks
// and successful preparation alone do not end its registered usage.
func (s *Server) QueueNeedsPreparation(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.prefetchSources[strconv.FormatInt(id, 10)]
	return q != nil && !q.expired && q.use.first == q.use.last
}

// QueuePreparationChanged closes when song preferences change or an expired
// member regains preparation eligibility. Subscribe before reading either state
// so changes during reconciliation cannot be lost. Capacity waits use their own
// existing notification; ordinary clock renewal needs no coordinator wakeup.
func (s *Server) QueuePreparationChanged() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queuePreparationChanged == nil {
		s.queuePreparationChanged = make(chan struct{})
	}
	return s.queuePreparationChanged
}

func (s *Server) notifyQueuePreparationLocked() {
	if s.queuePreparationChanged != nil {
		close(s.queuePreparationChanged)
		s.queuePreparationChanged = nil
	}
}

func (s *Server) rearmQueueSourceLocked(q *queueSource) {
	if q.expired {
		q.expired = false
		s.notifyQueuePreparationLocked()
	}
}

func (s *Server) queuePlaybackEndLocked() time.Time {
	var earliest time.Time
	for d := range s.demands {
		if d.origin == originPreview || d.clock == nil || !d.exit.active(d.ctx) {
			continue
		}
		end := d.clock.end()
		if earliest.IsZero() || end.Before(earliest) {
			earliest = end
		}
	}
	return earliest
}

// Update the common anchor on source events. Idle queue projections apply
// max(anchor, now) when used, so passing time alone never rewrites the anchor.
func (s *Server) retimeQueueLocked(now time.Time) []string {
	earliest := s.queuePlaybackEndLocked()
	rolling := earliest.IsZero()
	modeChanged := s.queueRolling != rolling
	s.queueRolling = rolling
	if !earliest.IsZero() {
		s.queueStart = earliest
	} else if len(s.prefetchOrder) > 0 && s.queueStart.IsZero() {
		s.queueStart = now
	}
	var changed []string
	start := s.queueStart
	for i, id := range s.prefetchOrder {
		duration := typicalDuration
		if q := s.prefetchSources[strconv.FormatInt(id, 10)]; q != nil {
			if q.use.key != "" {
				changed = append(changed, q.use.key)
			}
			duration = q.timing.duration
			if q.position == i {
				clockChanged := modeChanged || q.startAt != start
				if clockChanged {
					s.rearmQueueSourceLocked(q)
				}
				q.startAt = start
				if clockChanged && s.cfg.Logger != nil {
					s.cfg.Logger.Debug("队列播放时间推算", "event_name", "queue_playback_timing", "song_id", q.song, "position", i, "starts_at", s.queueClockLocked(q, now).at, "media_bytes", q.timing.size, "duration_seconds", q.timing.duration.Seconds())
				}
			}
		}
		start = start.Add(duration)
	}
	return changed
}

// Materialize the current song clock for a point-in-time event or snapshot.
func (s *Server) queueClockLocked(q *queueSource, now time.Time) playbackClock {
	at := q.startAt
	if s.queueRolling && now.After(s.queueStart) {
		at = now.Add(q.startAt.Sub(s.queueStart))
	}
	return playbackClock{at: at, media: q.timing}
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
		s.rearmQueueSourceLocked(q)
		q.timing = timing
		s.notifyUseChangedLocked(s.retimeQueueLocked(time.Now())...)
	}
	return nil
}
