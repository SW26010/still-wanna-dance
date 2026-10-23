package cacheproxy

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"still-wanna-dance/internal/blocksched"
)

// GET and queue-head handoffs share a fixed lifetime and source-clock shift.
const handoffGrace = 2 * time.Second

// Resource ownership feeds the block-demand adapter. It carries no scheduling rank.
var errPreempted = errors.Join(context.Canceled, errors.New("download stopped without active demand"))

type queueDemandKey struct{}
type sourceDemandKey struct{}
type httpGETKey struct{}

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
	use                useSpan
	expired            bool // Failed preparation; a real queue timing change may rearm it.
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
	ctx                   context.Context
	cancel                context.CancelCauseFunc
	flight                *flight // Attached download session, protected by Server.mu.
	exit                  sourceExit
	key, song             string
	queue, playback       bool
	queueSource           *queueSource
	clock                 *playbackClock
	firstBlock, lastBlock int64 // Remaining GET span, protected by Server.mu.
}

// sourceExit freezes withdrawal before registry locks or coalesced coordinator
// wakeups can delay it. It belongs to the original source, never to a handoff.
type sourceWithdrawal struct {
	at     time.Time
	reason withdrawalReason
}
type sourceExit struct {
	at atomic.Pointer[sourceWithdrawal]
}

func (e *sourceExit) recordReason(reason withdrawalReason) {
	if e.at.Load() == nil {
		e.at.CompareAndSwap(nil, &sourceWithdrawal{time.Now(), reason})
	}
}

func (e *sourceExit) active(ctx context.Context) bool {
	if ctx.Err() != nil {
		reason := withdrawalSourceGone
		if owned, _ := ctx.Value(httpGETKey{}).(bool); owned && !errors.Is(ctx.Err(), context.Canceled) {
			reason = withdrawalNeedEnded
		}
		e.recordReason(reason) // Planning can observe cancellation before AfterFunc runs.
	}
	return e.at.Load() == nil
}

func (e *sourceExit) when() time.Time {
	if at := e.at.Load(); at != nil {
		return at.at
	}
	return time.Time{}
}

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
	var changedKeys []string
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
				if source.timing != metadata.timing {
					s.rearmQueueSourceLocked(source)
				}
				source.timing, source.title = metadata.timing, metadata.title
				next[song] = source
			}
		}
	}
	for song, source := range previous {
		if next[song] != source {
			changedKeys = append(changedKeys, source.use.key)
			source.removedAt = now
			grace := time.Duration(0)
			if source.position == 0 {
				grace = handoffGrace
			}
			s.endUseLocked(source.use, now, grace, playbackClock{at: source.startAt.Add(handoffGrace), media: source.timing}, source.origin())
			source.use = useSpan{}
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
	changedKeys = append(changedKeys, s.retimeQueueLocked(now)...)
	s.notifyUseChangedLocked(changedKeys...)
	return nil
}
func (s *Server) addDemand(ctx context.Context, v video, playback bool) (*downloadDemand, func(), error) {
	if err := context.Cause(ctx); err != nil {
		return nil, nil, err
	}
	if existing, ok := ctx.Value(sourceDemandKey{}).(*downloadDemand); ok && existing != nil && existing.key == v.key && existing.playback == playback {
		if err := context.Cause(existing.ctx); err != nil {
			return nil, nil, err
		}
		return existing, func() {}, nil // The response/preparation owns this source's lifetime.
	}
	token, queue := ctx.Value(queueDemandKey{}).(*queueSource)
	d := &downloadDemand{ctx: ctx, key: v.key, song: v.songID, queue: queue, playback: playback, queueSource: token}
	if !playback {
		d.lastBlock = blockCount(v.size)
	}
	if playback {
		var observed bool
		d.clock, observed = ctx.Value(playbackClockKey{}).(*playbackClock)
		if !observed {
			d.clock = &playbackClock{at: time.Now(), limit: v.size, media: estimateMediaTiming(v.size, 0)}
		}
		if d.clock != nil {
			d.firstBlock, d.lastBlock = blocksForRange(d.clock.offset, d.clock.limit)
		}
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, context.Canceled
	}
	var previousKey string
	if queue {
		if s.validQueueSourceLocked(d) && token.expired {
			s.mu.Unlock()
			return nil, nil, blocksched.ErrExpired
		}
		if s.validQueueSourceLocked(d) && token.use != d.useSpan() {
			if err := s.replaceUseLocked(token.use, d.useSpan(), false); err != nil {
				s.mu.Unlock()
				return nil, nil, err
			}
			previousKey = token.use.key
			token.use = d.useSpan()
		}
	} else if err := s.reserveUseLocked(d.useSpan(), playback); err != nil {
		s.mu.Unlock()
		return nil, nil, err
	}
	if s.demands == nil {
		s.demands = make(map[*downloadDemand]bool)
	}
	d.ctx, d.cancel = context.WithCancelCause(ctx)
	s.demands[d] = true
	s.notifyUseChangedLocked(append(s.retimeQueueLocked(time.Now()), previousKey, d.key)...)
	s.mu.Unlock()
	release := sync.OnceFunc(func() {
		reason := withdrawalSourceGone
		if owned, _ := ctx.Value(httpGETKey{}).(bool); owned && !errors.Is(ctx.Err(), context.Canceled) {
			reason = withdrawalNeedEnded
		}
		d.exit.recordReason(reason)
		s.mu.Lock()
		s.removeDemandLocked(d)
		s.mu.Unlock()
		d.cancel(nil)
	})
	stop := context.AfterFunc(ctx, release)
	return d, func() { stop(); release() }, nil
}

// Cancellation can be observed before its AfterFunc obtains mu. Transfer the
// remaining usage atomically whichever observer gets here first; a snapshot
// must never see a false gap between a GET and its handoff.
func (s *Server) removeDemandLocked(d *downloadDemand) {
	if !s.demands[d] {
		return
	}
	if !d.queue {
		grace := time.Duration(0)
		var clock playbackClock
		if d.playback && d.clock != nil && d.exit.at.Load().reason == withdrawalSourceGone {
			grace = handoffGrace
			clock = *d.clock
			clock.at = clock.at.Add(handoffGrace)
		}
		s.endUseLocked(d.useSpan(), d.exit.when(), grace, clock, sourceGET)
	}
	delete(s.demands, d)
	s.notifyUseChangedLocked(append(s.retimeQueueLocked(time.Now()), d.key)...)
}
func (s *Server) notifyUseChangedLocked(keys ...string) {
	changed := make(map[string]bool, len(keys))
	for _, key := range keys {
		if key != "" && !changed[key] {
			s.refreshUseProtectionLocked(key)
			changed[key] = true
		}
	}
	// Even a delayed source exit first establishes its frozen handoff clock,
	// then ends it at the original deadline. No callback delay extends grace.
	expired := s.expireUseLocked(time.Now())
	for _, key := range expired {
		changed[key] = true
	}
	// Expiry may affect the same resource as the mutation. Rebuild it after
	// establishing the handoff's frozen clock and ending its final references.
	for _, key := range uniqueResourceKeys(expired) {
		s.refreshUseProtectionLocked(key)
	}
	// Source mutations publish their final clock/expiry and arm the shared timer
	// together. Timer callbacks use this same path when they change sources.
	s.armUseTimerLocked()
	for key := range changed {
		s.notifyResourceChangedLocked(key)
	}
	s.notifyBlockObservation()
}

func uniqueResourceKeys(keys []string) []string {
	seen := make(map[string]bool, len(keys))
	result := keys[:0]
	for _, key := range keys {
		if key != "" && !seen[key] {
			seen[key] = true
			result = append(result, key)
		}
	}
	return result
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
