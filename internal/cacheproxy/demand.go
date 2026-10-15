package cacheproxy

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Resource ownership feeds the block-demand adapter. It carries no scheduling rank.
var errPreempted = errors.Join(context.Canceled, errors.New("download paused without active demand"))

type queueDemandKey struct{}

// Identity lasts for one continuous queue membership. Mutable fields are
// protected by Server.mu. Removal freezes the last position and its time, so
// coalesced wakeups cannot lose the source at withdrawal. Only existing demand
// references retain removed members; no transition history is accumulated.
type queueSource struct {
	song               string
	position           int
	startAt, removedAt time.Time
	timing             mediaTiming
	title              string
}

func (q *queueSource) origin() demandSource {
	if q.position == 0 {
		return sourceQueueHead
	}
	return sourceQueue
}

func (q *queueSource) readyBy(block int64) time.Time {
	return q.startAt.Add(q.timing.at(block * rangeBlockSize))
}

type downloadDemand struct {
	ctx             context.Context
	exit            sourceExit
	key, song       string
	queue, playback bool
	queueSource     *queueSource
	clock           *playbackClock
}

// sourceExit freezes withdrawal before registry locks or coalesced coordinator
// wakeups can delay it. It belongs to the original source, never to a handoff.
type sourceExit struct{ at atomic.Pointer[time.Time] }

func (e *sourceExit) record() {
	if e.at.Load() == nil {
		now := time.Now()
		e.at.CompareAndSwap(nil, &now)
	}
}

func (e *sourceExit) active(ctx context.Context) bool {
	if ctx.Err() != nil {
		e.record() // Planning can observe cancellation before AfterFunc runs.
	}
	return e.at.Load() == nil
}

func (e *sourceExit) when() time.Time {
	if at := e.at.Load(); at != nil {
		return *at
	}
	return time.Time{}
}

func (s *Server) queueContainsLocked(song string) bool { return s.prefetchSources[song] != nil }
func (s *Server) UpdatePrefetchQueue(ids []int64, reset bool) error {
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	now := time.Now()
	timings, err := s.queueTimings(context.Background(), ids)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.prefetchSources
	next := make(map[string]*queueSource, len(ids))
	changed := reset || len(ids) != len(s.prefetchOrder)
	for i, id := range ids {
		if i >= len(s.prefetchOrder) || s.prefetchOrder[i] != id {
			changed = true
		}
	}
	for i, id := range ids {
		if id > 0 {
			song := strconv.FormatInt(id, 10)
			if next[song] == nil {
				source := previous[song]
				if reset || source == nil {
					source = &queueSource{song: song}
					changed = true
				}
				changed = changed || source.position != i
				source.position = i
				metadata, ok := timings[song]
				if !ok {
					metadata.timing = estimateMediaTiming(0, 0)
				}
				changed = changed || source.timing != metadata.timing || source.title != metadata.title
				source.timing, source.title = metadata.timing, metadata.title
				next[song] = source
			}
		}
	}
	for song, source := range previous {
		if next[song] != source {
			source.removedAt = now
			changed = true
		}
	}
	if !changed {
		return nil // Repeated snapshots must not postpone queue deadlines.
	}
	s.prefetchSources = next
	s.prefetchOrder = append([]int64(nil), ids...)
	if reset {
		s.queueStart = time.Time{}
	}
	s.retimeQueueLocked(now)
	for song := range s.prefetchResources {
		if reset || !s.queueContainsLocked(song) {
			delete(s.prefetchResources, song)
		}
	}
	s.notifyCapacityLocked()
	return nil
}
func (s *Server) addDemand(ctx context.Context, v video, playback bool) (*downloadDemand, func()) {
	token, queue := ctx.Value(queueDemandKey{}).(*queueSource)
	d := &downloadDemand{ctx: ctx, key: v.key, song: v.songID, queue: queue, playback: playback, queueSource: token}
	if playback {
		var observed bool
		d.clock, observed = ctx.Value(playbackClockKey{}).(*playbackClock)
		if !observed {
			d.clock = &playbackClock{at: time.Now(), media: estimateMediaTiming(v.size, 0)}
		}
	}
	s.mu.Lock()
	if s.demands == nil {
		s.demands = make(map[*downloadDemand]bool)
	}
	s.demands[d] = true
	s.retimeQueueLocked(time.Now())
	if queue && ctx.Err() == nil && s.validQueueSourceLocked(d) {
		if s.prefetchResources == nil {
			s.prefetchResources = make(map[string]string)
		}
		s.prefetchResources[v.songID] = v.key
	}
	s.notifyCapacityLocked()
	s.mu.Unlock()
	release := sync.OnceFunc(func() {
		d.exit.record()
		s.mu.Lock()
		delete(s.demands, d)
		s.retimeQueueLocked(time.Now())
		s.notifyCapacityLocked()
		s.mu.Unlock()
	})
	stop := context.AfterFunc(ctx, release)
	return d, func() { stop(); release() }
}
func (s *Server) notifyCapacityLocked() {
	close(s.capacityChanged)
	s.capacityChanged = make(chan struct{})
}

func (s *Server) queueDemandContext(ctx context.Context, song string) context.Context {
	s.mu.Lock()
	token := s.prefetchSources[song]
	s.mu.Unlock()
	return context.WithValue(ctx, queueDemandKey{}, token)
}
func (s *Server) validQueueSourceLocked(d *downloadDemand) bool {
	return d.queueSource != nil && d.queueSource.song == d.song && s.prefetchSources[d.song] == d.queueSource
}
