package cacheproxy

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestResumedForegroundClaimsStartupBeforeReadAhead(t *testing.T) {
	for _, closeReaders := range []bool{false, true} {
		name := "readers-open"
		if closeReaders {
			name = "readers-closed"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			w := newRangeWork(6 * rangeBlockSize)
			// Match a foreground resume: no initial response owns block 0,
			// and only blocks 1 and 3 have already been completed.
			delete(w.claimed, 0)
			w.startup = true
			w.finish(1)
			w.finish(3)
			readerCtx, cancelReaders := context.WithCancel(ctx)
			defer cancelReaders()
			w.trackReader(readerCtx)(rangeBlockSize)
			w.trackReader(readerCtx)(3 * rangeBlockSize)
			// Reads from saved blocks create hints but no missing-block demand,
			// so the dedicated reader worker cannot rescue the startup block.
			var claimed []int64
			for range rangeBackgroundWorkers {
				block, _, ok := w.next(ctx, true)
				if !ok {
					t.Fatal("no background claim")
				}
				claimed = append(claimed, block)
			}
			if claimed[0] != 0 {
				t.Fatalf("startup block must precede speculative read-ahead: claimed=%v", claimed)
			}
			if closeReaders {
				cancelReaders()
			}
			complete := func(block int64) {
				t.Helper()
				_, finish, err := w.beginTransfer(ctx, block)
				if err != nil {
					t.Fatalf("block %d stalled: %v", block, err)
				}
				finish()
				w.finish(block)
			}
			// Finishing block 0 must release the other queued workers even if
			// the readers have closed, and background work must fill all holes.
			for _, block := range claimed {
				complete(block)
			}
			for {
				block, _, ok := w.next(ctx, true)
				if !ok {
					break
				}
				complete(block)
			}
			if w.complete != w.blocks {
				t.Fatalf("download stalled with %d/%d complete blocks", w.complete, w.blocks)
			}
		})
	}
}

func TestResumedForegroundTailDemandBypassesStartup(t *testing.T) {
	for _, background := range []bool{true, false} {
		name := "background-worker"
		if !background {
			name = "reader-worker"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			w := newRangeWork(6 * rangeBlockSize)
			delete(w.claimed, 0)
			w.startup = true
			w.trackReader(ctx)(5 * rangeBlockSize)
			release := w.demand(5 * rangeBlockSize)
			defer release()
			block, blockCtx, ok := w.next(ctx, background)
			if !ok || block != 5 {
				t.Fatalf("tail demand must precede startup: block=%d, ok=%v", block, ok)
			}
			startup, startupCtx, ok := w.next(ctx, true)
			if !ok || startup != 0 {
				t.Fatalf("startup block must have an owner: block=%d, ok=%v", startup, ok)
			}
			_, finish, err := w.beginTransfer(blockCtx, block)
			if err != nil {
				t.Fatal("tail read waited for incomplete startup block:", err)
			}
			finish()
			w.finish(block)
			release()
			_, finish, err = w.beginTransfer(startupCtx, startup)
			if err != nil {
				t.Fatal("startup did not resume after tail read:", err)
			}
			finish()
			w.finish(startup)
		})
	}
}

func TestResumedForegroundKeepsBackgroundStartupOwnerAfterReaderCloses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w := newRangeWork(6 * rangeBlockSize)
	delete(w.claimed, 0)
	w.startup = true
	w.finish(1)
	w.finish(3)
	w.trackReader(ctx)(rangeBlockSize)
	w.trackReader(ctx)(3 * rangeBlockSize)
	release := w.demand(0)
	defer release()
	block, blockCtx, ok := w.next(ctx, true)
	if !ok || block != 0 {
		t.Fatal("background worker did not claim startup block")
	}
	var queued []int64
	for range rangeBackgroundWorkers - 1 {
		next, _, ok := w.next(ctx, true)
		if !ok {
			t.Fatal("no background claim")
		}
		queued = append(queued, next)
	}
	// The other background workers now depend on this worker's block.
	// Releasing its only reader must not leave block 0 without an owner.
	release()
	_, finish, err := w.beginTransfer(blockCtx, block)
	if err != nil {
		t.Fatalf("startup owner canceled after reader closed: %v (queued=%v)", err, queued)
	}
	finish()
	w.finish(block)
	for _, next := range queued {
		_, finish, err := w.beginTransfer(ctx, next)
		if err != nil {
			t.Fatalf("queued block %d stalled: %v", next, err)
		}
		finish()
		w.finish(next)
	}
	// The startup worker is available to fill the remaining hole.
	last, lastCtx, ok := w.next(ctx, true)
	if !ok {
		t.Fatal("missing final background claim")
	}
	_, finish, err = w.beginTransfer(lastCtx, last)
	if err != nil {
		t.Fatal("final block stalled:", err)
	}
	finish()
	w.finish(last)
	if w.complete != w.blocks {
		t.Fatalf("download stalled with %d/%d complete blocks", w.complete, w.blocks)
	}
}

func TestResumedForegroundReservesReaderForLaterTailSeek(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w := newRangeWork(7 * rangeBlockSize)
	delete(w.claimed, 0)
	w.startup = true
	w.finish(1)
	w.finish(3)
	w.trackReader(ctx)(rangeBlockSize)
	w.trackReader(ctx)(3 * rangeBlockSize)
	releaseFirst := w.demand(0)
	defer releaseFirst()

	// Let the reader worker select first, while startup is still unclaimed.
	// It must handle the newer tail demand instead of becoming the startup
	// owner; that owner may later have to yield to another tail Seek.
	w.trackReader(ctx)(5 * rangeBlockSize)
	releaseTail := w.demand(5 * rangeBlockSize)
	defer releaseTail()
	tail, tailCtx, ok := w.next(ctx, false)
	if !ok || tail != 5 {
		t.Fatalf("reader worker took startup instead of tail: block=%d, ok=%v", tail, ok)
	}
	_, finishTail, err := w.beginTransfer(tailCtx, tail)
	if err != nil {
		t.Fatal("initial tail read stalled:", err)
	}
	finishTail()
	w.finish(tail)
	releaseTail()

	first, firstCtx, ok := w.next(ctx, true)
	if !ok || first != 0 {
		t.Fatal("background worker did not claim startup")
	}
	transferCtx, finishFirst, err := w.beginTransfer(firstCtx, first)
	if err != nil {
		t.Fatal(err)
	}
	var queued []int64
	for range rangeBackgroundWorkers - 1 {
		block, _, ok := w.next(ctx, true)
		if !ok {
			t.Fatal("no background claim")
		}
		queued = append(queued, block)
	}
	releaseFirst()
	w.trackReader(ctx)(6 * rangeBlockSize)
	releaseLaterTail := w.demand(6 * rangeBlockSize)
	defer releaseLaterTail()
	if !errors.Is(context.Cause(transferCtx), errRangeYielded) {
		t.Fatal("startup transfer did not yield to later Seek")
	}
	finishFirst()

	// All three background workers are now waiting, but the reader worker
	// remains available to break the dependency by completing the tail.
	tail, tailCtx, ok = w.next(ctx, false)
	if !ok || tail != 6 {
		t.Fatal("reader worker could not claim later tail")
	}
	_, finishTail, err = w.beginTransfer(tailCtx, tail)
	if err != nil {
		t.Fatal("later tail waited behind startup:", err)
	}
	finishTail()
	w.finish(tail)
	releaseLaterTail()
	_, finishFirst, err = w.beginTransfer(firstCtx, first)
	if err != nil {
		t.Fatal("startup failed to resume after later Seek:", err)
	}
	finishFirst()
	w.finish(first)
	for _, block := range queued {
		_, finish, err := w.beginTransfer(ctx, block)
		if err != nil {
			t.Fatalf("queued block %d stalled: %v", block, err)
		}
		finish()
		w.finish(block)
	}
	if w.complete != w.blocks {
		t.Fatalf("download stalled with %d/%d complete blocks", w.complete, w.blocks)
	}
}
