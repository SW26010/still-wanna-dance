package cacheproxy

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"still-wanna-dance/internal/vrclog"
)

// All handoffs share a fixed lifetime, retain the source clock and use the same
// closed-admission projection.
const handoffGrace = 2 * time.Second

// Resource ownership feeds the block-demand adapter.
var errPreempted = errors.Join(context.Canceled, errors.New("download stopped without active demand"))

type queueDemandKey struct{}
type sourceDemandKey struct{}
type httpGETKey struct{}

type playbackOrigin string

const originPreview playbackOrigin = "preview"

type playbackOriginKey struct{}

// Identity lasts for one continuous queue membership. Mutable fields are
// protected by Server.mu. Removal freezes the last position and its time, so
// coalesced wakeups cannot lose the source at withdrawal. Only existing demand
// references retain removed members; no transition history is accumulated.
type queueSource struct {
	song     string
	position int
	startAt  time.Time
	timing   mediaTiming
	fixed    mediaTiming // The published media clock while the schedule is held.
	title    string
	use      useSpan // Expiry advances first; last preserves the resolved end for readmission.
}

func (q *queueSource) origin() demandSource {
	if q.position == 0 {
		return sourceQueueHead
	}
	return sourceQueue
}

func (q *queueSource) readyBy(block, blockSize int64) time.Time {
	clock := q.clock()
	return clock.ready(block * blockSize)
}

type downloadDemand struct {
	lookup                resourceAddresses // Initial address policy and any already-started song check.
	ctx                   context.Context
	cancel                context.CancelCauseFunc
	flight                *flight // Attached download session, protected by Server.mu.
	exit                  sourceExit
	key, song             string
	queue, playback       bool
	origin                playbackOrigin // Request identity; never a property of a shared resource.
	preparationOrder      uint64         // Stable across this logical preparation's retries.
	queueSource           *queueSource
	clock                 *playbackClock
	firstBlock, lastBlock int64 // Remaining GET span, protected by Server.mu.
}

func (d *downloadDemand) gameSongID() string {
	if d.playback && d.origin != originPreview {
		return d.song
	}
	return ""
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

func (s *Server) UpdatePrefetchQueue(ids []int64, reset bool) error {
	return s.UpdatePrefetchQueueWithRoute(ids, reset, 0)
}

// routedSong is the catalog ID confirmed by the room alongside this snapshot.
func (s *Server) UpdatePrefetchQueueWithRoute(ids []int64, reset bool, routedSong int64) error {
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	received := time.Now()
	timings, err := s.queueTimings(context.Background(), ids)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.refreshUseSourcesLocked(now)
	if s.awaitEntryQueue && len(ids) > 0 {
		s.awaitEntryQueue = false
		s.queueWaitUntil = received.Add(roomEntryQueueWait)
	}
	previous := s.prefetchSources
	var changedKeys []string
	next := make(map[string]*queueSource, len(ids))
	changed := reset || len(ids) != len(s.prefetchOrder)
	for i, id := range ids {
		if i >= len(s.prefetchOrder) || s.prefetchOrder[i] != id {
			changed = true
		}
	}
	selected := s.selectedRoomSong
	selectedDeparted := selected != 0 && vrclog.SelectedSongDeparted(s.prefetchOrder, ids, selected, -1)
	routedHeadDeparted := routedSong > 0 && len(s.prefetchOrder) > 0 && s.prefetchOrder[0] == routedSong && !slices.Contains(ids, routedSong)
	selectedHeadDeparted := selectedDeparted && s.prefetchOrder[0] == selected
	holdSuccessors := !reset && len(ids) > 0 && s.queueSchedule == queueRolling && (selectedHeadDeparted || routedHeadDeparted)
	if reset || selectedDeparted {
		s.selectedRoomSong = 0
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
				next[song] = source
			}
		}
	}
	for song, source := range previous {
		if next[song] != source {
			changedKeys = append(changedKeys, source.use.key)
			grace := time.Duration(0)
			if source.position == 0 {
				grace = handoffGrace
			}
			clock := s.queueClockLocked(source, now)
			source.startAt = clock.at
			s.endUseLocked(source.use, now, grace, clock, source.origin(), withdrawalSourceGone, "")
			source.use = useSpan{}
			changed = true
		}
	}
	if holdSuccessors {
		// Preserve the surviving slots at the instant before the head
		// departed. Newly added sources receive their slots after the old tail.
		s.holdQueueScheduleLocked(maxTime(s.queueStart, now), next)
	}
	for song, source := range next {
		metadata, ok := timings[song]
		if !ok {
			metadata.timing = estimateMediaTiming(0, 0)
		}
		changed = changed || source.timing != metadata.timing || source.title != metadata.title
		source.timing, source.title = metadata.timing, metadata.title
	}
	if !changed {
		return nil // Repeated snapshots must not postpone queue deadlines.
	}
	s.prefetchSources = next
	s.prefetchOrder = append([]int64(nil), ids...)
	// A queue-only reset can occur during external playback. The room's
	// independent stop/reset event clears its clock.
	if s.queueSchedule != queueExternal && (reset || len(ids) == 0) {
		s.resetQueueScheduleLocked(now)
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
	d.lookup = resourceAddresses{key: v.key, size: v.size, preferredSong: v.songID}
	if v.lookup != nil {
		d.lookup = *v.lookup
	}
	d.lookup.preferredCheck = v.refresh
	d.origin, _ = ctx.Value(playbackOriginKey{}).(playbackOrigin)
	if !playback {
		d.lastBlock = blockCount(v.size, s.cfg.BlockSize)
	}
	if playback {
		var observed bool
		d.clock, observed = ctx.Value(playbackClockKey{}).(*playbackClock)
		if !observed {
			d.clock = &playbackClock{at: time.Now(), limit: v.size, media: estimateMediaTiming(v.size, 0)}
		}
		if d.clock != nil {
			d.firstBlock, d.lastBlock = blocksForRange(d.clock.offset, d.clock.limit, s.cfg.BlockSize)
		}
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, context.Canceled
	}
	var previousKey string
	if queue {
		if s.validQueueSourceLocked(d) {
			now := time.Now()
			s.refreshUseSourcesLocked(now)
			span := d.useSpan()
			if s.queueSchedule == queueFixed {
				for span.first < span.last && sourceExpired(token.readyBy(span.first, s.cfg.BlockSize), now) {
					span.first++
				}
			}
			if token.use != span {
				if err := s.swapUseLocked(token.use, span, false); err != nil {
					s.mu.Unlock()
					return nil, nil, err
				}
				previousKey = token.use.key
				token.use = span
			}
		}
	} else if err := s.reserveUseLocked(d.useSpan(), playback); err != nil {
		s.mu.Unlock()
		return nil, nil, err
	}
	if !queue && !playback {
		order, err := s.preparationOrderLocked(ctx)
		if err != nil {
			s.releaseUseLocked(d.useSpan(), time.Now())
			s.mu.Unlock()
			return nil, nil, err
		}
		d.preparationOrder = order
	}
	if s.demands == nil {
		s.demands = make(map[*downloadDemand]bool)
	}
	d.ctx, d.cancel = context.WithCancelCause(ctx)
	s.demands[d] = true
	s.notifyUseChangedLocked(previousKey, d.key)
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
		span := d.useSpan()
		exit := d.exit.at.Load()
		grace := time.Duration(0)
		var clock playbackClock
		if d.playback && d.clock != nil {
			clock = *d.clock
			switch exit.reason {
			case withdrawalSourceGone, withdrawalDownloadExpired:
				grace = handoffGrace
			}
		}
		s.endUseLocked(span, exit.at, grace, clock, sourceGET, exit.reason, d.gameSongID())
	}
	delete(s.demands, d)
	s.notifyUseChangedLocked(d.key)
}

// Consumption changes only the delivered prefix. Keep reference release,
// protection and publication together, so HTTP need only report its progress.
func (s *Server) consumeDemand(d *downloadDemand, next int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Withdrawal may have already transferred/released the remaining span
	// while the response writer was returning.
	if !s.demands[d] {
		return
	}
	span := useSpan{d.key, d.firstBlock, next}
	s.releaseUseLocked(span, time.Now())
	d.firstBlock = next
	s.refreshUseProtectionLocked(span)
	// Consumption advances the GET's prefix without changing its lookup scope.
	s.notifyResourceChangedLocked(d.key)
	s.publishUseChangedLocked()
}

func (s *Server) notifyUseChangedLocked(keys ...string) {
	keys = uniqueResourceKeys(keys)
	for _, key := range keys {
		s.refreshUseProtectionLocked(allUseBlocks(key))
	}
	s.publishUseChangedLocked(keys...)
}

func (s *Server) publishUseChangedLocked(keys ...string) {
	// Even a delayed source exit first establishes its frozen handoff clock,
	// then ends it at the original deadline. No callback delay extends grace.
	expired := s.expireUseLocked(time.Now())
	// Source mutations publish their final clock/expiry and arm the shared timer
	// together. Timer callbacks use this same path when they change sources.
	s.armUseTimerLocked()
	for _, key := range uniqueResourceKeys(append(keys, expired...)) {
		if f := s.flights[key]; f != nil {
			s.syncAddressLookupsLocked(f, false)
		}
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
