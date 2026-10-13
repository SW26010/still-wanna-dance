package cacheproxy

import (
	"context"
	"sync"
)

const rangeBackgroundWorkers = 3
const rangeReadAheadBlocks int64 = 2

// One worker is reserved for missing reader blocks; background workers also
// help readers before filling their small read-ahead windows and other holes.
// Claimed blocks include queued requests, so a later reader promotes the same
// request rather than starting a duplicate. Claims last through Body.Close.
type rangeWork struct {
	mu               sync.Mutex
	claimed, done    map[int64]bool
	readers          map[int64]int
	order            map[int64]uint64
	sequence         uint64
	active           map[int64]context.CancelCauseFunc
	cursors          map[*rangeCursor]bool
	changed          chan struct{}
	blocks, complete int64
}

type rangeCursor struct {
	ctx   context.Context
	block int64
}

func newRangeWork(size int64) *rangeWork {
	return &rangeWork{
		claimed: map[int64]bool{0: true}, done: make(map[int64]bool),
		readers: make(map[int64]int), order: make(map[int64]uint64),
		active: make(map[int64]context.CancelCauseFunc), cursors: make(map[*rangeCursor]bool),
		changed: make(chan struct{}), blocks: (size + rangeBlockSize - 1) / rangeBlockSize,
	}
}

func (w *rangeWork) signalLocked() { close(w.changed); w.changed = make(chan struct{}) }

// trackReader keeps only a byte-position hint, not a prediction of game playback
// or seconds buffered. Hints disappear on Close/cancellation and move on reads.
func (w *rangeWork) trackReader(ctx context.Context) func(int64) {
	cursor := &rangeCursor{ctx: ctx, block: -1}
	w.mu.Lock()
	w.cursors[cursor] = true
	w.mu.Unlock()
	context.AfterFunc(ctx, func() {
		w.mu.Lock()
		delete(w.cursors, cursor)
		w.signalLocked()
		w.mu.Unlock()
	})
	return func(offset int64) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if block := offset / rangeBlockSize; ctx.Err() == nil && cursor.block != block {
			cursor.block = block
			w.signalLocked()
		}
	}
}

func (w *rangeWork) demand(offset int64) func() {
	w.mu.Lock()
	defer w.mu.Unlock()
	block := offset / rangeBlockSize
	if offset < 0 || block >= w.blocks || w.done[block] {
		return func() {}
	}
	if w.readers[block] == 0 {
		w.sequence++
		w.order[block] = w.sequence
	}
	w.readers[block]++
	w.signalLocked()
	return sync.OnceFunc(func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.readers[block]--
		if w.readers[block] == 0 {
			delete(w.readers, block)
			delete(w.order, block)
			if cancel := w.active[block]; cancel != nil {
				cancel(errRangeAbandoned)
			}
		}
		w.signalLocked()
	})
}

func (w *rangeWork) requestContext(ctx context.Context, block int64) context.Context {
	return context.WithValue(ctx, requestPriorityKey{}, requestPriority(func() (bool, <-chan struct{}) {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.readers[block] > 0, w.changed
	}))
}

func (w *rangeWork) next(ctx context.Context, background bool) (int64, context.Context, bool) {
	for {
		w.mu.Lock()
		if ctx.Err() != nil {
			w.mu.Unlock()
			return 0, ctx, false
		}
		var block int64 = -1
		// FIFO demands prevent readers at low offsets from starving a tail Seek.
		for b, order := range w.order {
			if !w.claimed[b] && !w.done[b] && (block < 0 || order < w.order[block]) {
				block = b
			}
		}
		demanded := block >= 0
		if block < 0 && background {
			// Prefer proximity, then offset for deterministic ties. These remain
			// speculative requests and cannot consume the global reader reserve.
			for distance := int64(1); distance <= rangeReadAheadBlocks && block < 0; distance++ {
				for cursor := range w.cursors {
					b := cursor.block + distance
					if cursor.ctx.Err() == nil && cursor.block >= 0 && b < w.blocks && !w.done[b] && !w.claimed[b] && (block < 0 || b < block) {
						block = b
					}
				}
			}
			if block < 0 {
				for b := int64(0); b < w.blocks; b++ {
					if !w.done[b] && !w.claimed[b] {
						block = b
						break
					}
				}
			}
		}
		if block >= 0 {
			w.claimed[block] = true
			blockCtx := ctx
			if demanded {
				var cancel context.CancelCauseFunc
				blockCtx, cancel = context.WithCancelCause(ctx)
				w.active[block] = cancel
			}
			w.mu.Unlock()
			return block, w.requestContext(blockCtx, block), true
		}
		finished, changed := w.complete == w.blocks, w.changed
		w.mu.Unlock()
		if finished {
			return 0, ctx, false
		}
		select {
		case <-ctx.Done():
			return 0, ctx, false
		case <-changed:
		}
	}
}

func (w *rangeWork) finish(block int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done[block] = true
	w.complete++
	delete(w.claimed, block)
	if cancel := w.active[block]; cancel != nil {
		cancel(nil)
		delete(w.active, block)
	}
	w.signalLocked()
}

func (w *rangeWork) abandon(block int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.claimed, block)
	delete(w.active, block)
	// Reader/order entries survive until their own release: a new reader may
	// arrive while the old canceled request is still closing.
	w.signalLocked()
}
