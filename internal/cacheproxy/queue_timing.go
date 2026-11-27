package cacheproxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"
)

type queueSchedule uint8

const (
	queueRolling  queueSchedule = iota
	queueFormal                 // The published formal clock supplies queueStart.
	queueExternal               // A confirmed external start supplies the rolling queue estimate.
	queueFixed                  // Keep absolute deadlines until the next valid room clock or stop.
)

const queueSongGap = 10 * time.Second

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

// QueuePreparationChanged closes when song preferences change. Subscribe before
// reading them so changes during reconciliation cannot be lost. Capacity waits
// use their own notification.
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

// A held schedule preserves absolute deadlines for surviving members.
// A rolling schedule evaluates max(base, now) without republishing.
func (s *Server) retimeQueueLocked(now time.Time) []string {
	if s.queueSchedule == queueFixed {
		return s.extendHeldQueueLocked(now)
	}
	if s.queueStart.IsZero() {
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
				clockChanged := q.startAt != start
				q.startAt = start
				q.fixed = mediaTiming{}
				if clockChanged && s.cfg.Logger != nil {
					s.cfg.Logger.Debug("队列播放时间推算", "event_name", "queue_playback_timing", "song_id", q.song, "position", i, "starts_at", s.queueClockLocked(q, now).at, "media_bytes", q.timing.size, "duration_seconds", q.timing.duration.Seconds())
				}
			}
		}
		start = start.Add(duration + queueSongGap)
	}
	return changed
}

func (s *Server) holdQueueScheduleLocked(base time.Time, sources map[string]*queueSource) {
	shift := base.Sub(s.queueStart)
	for _, q := range sources {
		if !q.startAt.IsZero() {
			q.startAt = q.startAt.Add(shift)
			q.fixed = q.timing
		}
	}
	s.queueSchedule = queueFixed
}

func (s *Server) retireQueueClockLocked(now time.Time) []string {
	if s.queueSchedule == queueExternal {
		// The queued songs keep their rolling base until playback provides a new clock.
		s.queueSchedule = queueRolling
		if len(s.prefetchOrder) == 0 {
			s.queueStart = now
		}
		return nil
	}
	if s.queueSchedule != queueFormal {
		return nil
	}
	if len(s.prefetchSources) == 0 {
		s.resetQueueScheduleLocked(now)
		return nil
	}
	s.holdQueueScheduleLocked(s.queueStart, s.prefetchSources)
	return s.retimeQueueLocked(now)
}

func (s *Server) resetQueueScheduleLocked(now time.Time) {
	s.queueSchedule, s.queueStart = queueRolling, now
	if p := s.formal; p != nil {
		s.queueSchedule, s.queueStart = queueFormal, p.clock.end().Add(queueSongGap)
	}
}

// Existing members keep their published slots through edits. Schedule new
// members after the retained slots, in snapshot order.
func (s *Server) extendHeldQueueLocked(now time.Time) []string {
	tail := now
	lastFixed := -1
	for i, id := range s.prefetchOrder {
		if q := s.prefetchSources[strconv.FormatInt(id, 10)]; q != nil && q.position == i && q.fixed.duration > 0 {
			clock := q.clock()
			tail = maxTime(tail, clock.end().Add(queueSongGap))
			lastFixed = i
		}
	}
	var keys []string
	for i, id := range s.prefetchOrder {
		q := s.prefetchSources[strconv.FormatInt(id, 10)]
		if q == nil {
			if i > lastFixed {
				tail = tail.Add(typicalDuration + queueSongGap)
			}
			continue
		}
		if q.position != i {
			if i > lastFixed {
				tail = tail.Add(q.clock().media.duration + queueSongGap)
			}
			continue
		}
		if q.fixed.duration == 0 {
			q.startAt, q.fixed = tail, q.timing
			tail = tail.Add(q.fixed.duration + queueSongGap)
		}
		keys = append(keys, q.use.key)
	}
	return keys
}

func (q *queueSource) clock() playbackClock {
	media := q.timing
	if q.fixed.duration > 0 {
		media = q.fixed
	}
	return playbackClock{at: q.startAt, media: media}
}

// Materialize the current song clock for a point-in-time event or snapshot.
func (s *Server) queueClockLocked(q *queueSource, now time.Time) playbackClock {
	clock := q.clock()
	if s.queueSchedule != queueFixed && now.After(s.queueStart) {
		clock.at = now.Add(q.startAt.Sub(s.queueStart))
	}
	return clock
}

func (s *Server) firstQueueExpiryLocked() (*queueSource, time.Time) {
	var source *queueSource
	var next time.Time
	for _, q := range s.prefetchSources {
		if q.use.first < q.use.last {
			at := sourceDeadline(q.readyBy(q.use.first, s.cfg.BlockSize))
			if next.IsZero() || at.Before(next) {
				source, next = q, at
			}
		}
	}
	return source, next
}

func (s *Server) nextQueueExpiryLocked() time.Time {
	if s.queueSchedule != queueFixed {
		return time.Time{}
	}
	_, at := s.firstQueueExpiryLocked()
	return at
}

func (s *Server) expireQueueUseLocked(now time.Time) []string {
	var keys []string
	if s.queueSchedule == queueFixed {
		for {
			q, at := s.firstQueueExpiryLocked()
			if q == nil || at.After(now) {
				break
			}
			s.releaseUseLocked(useSpan{q.use.key, q.use.first, q.use.first + 1}, at)
			q.use.first++
			keys = append(keys, q.use.key)
		}
	}
	return keys
}

// Restore the released prefix in queue order through normal capacity checks.
// A failed restoration is retried on the next source release.
func (s *Server) restoreQueueUseLocked() []string {
	var keys []string
	for _, id := range s.prefetchOrder {
		q := s.prefetchSources[strconv.FormatInt(id, 10)]
		if q == nil || q.use.first == 0 {
			continue
		}
		full := useSpan{q.use.key, 0, q.use.last}
		if err := s.swapUseLocked(q.use, full, false); err != nil {
			break
		}
		q.use = full
		keys = append(keys, full.key)
	}
	return keys
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
		q.timing = timing
		s.notifyUseChangedLocked(s.retimeQueueLocked(time.Now())...)
	}
	return nil
}
