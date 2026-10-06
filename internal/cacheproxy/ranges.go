package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"still-wanna-dance/internal/applog"
)

const rangeBlockSize int64 = 1 << 20

var errRangeAbandoned = errors.New("range has no waiting readers")

func validateRangeResponse(r *http.Response, start, end, size int64) error {
	if r.StatusCode != http.StatusPartialContent || r.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", start, end-1, size) {
		return errors.New("upstream returned an invalid or unsupported byte range")
	}
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return errors.New("encoded byte range")
	}
	if r.ContentLength >= 0 && r.ContentLength != end-start {
		return errors.New("byte range length mismatch")
	}
	return nil
}

// Two workers per flight bound global requests to 2*MaxDownloads. One worker
// always remains available for reader demand while the other fills holes.
// Block claims coalesce overlapping requests without a goroutine per reader.
type rangeWork struct {
	mu               sync.Mutex
	claimed, done    map[int64]bool
	pending          map[int64]bool
	readers          map[int64]int
	active           map[int64]context.CancelCauseFunc
	changed          chan struct{}
	blocks, complete int64
}

func (w *rangeWork) signalLocked() { close(w.changed); w.changed = make(chan struct{}) }
func (w *rangeWork) demand(offset int64) func() {
	w.mu.Lock()
	defer w.mu.Unlock()
	block := offset / rangeBlockSize
	if block < 0 || block >= w.blocks || w.done[block] {
		return func() {}
	}
	w.readers[block]++
	if !w.claimed[block] {
		w.pending[block] = true
	}
	w.signalLocked()
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.readers[block]--
		if w.readers[block] == 0 {
			delete(w.readers, block)
			delete(w.pending, block)
			if cancel := w.active[block]; cancel != nil {
				cancel(errRangeAbandoned)
			}
		}
	}
}
func (w *rangeWork) next(ctx context.Context, background bool) (int64, context.Context, bool) {
	for {
		w.mu.Lock()
		var block int64 = -1
		for b := range w.pending {
			if !w.claimed[b] && !w.done[b] && (block < 0 || b < block) {
				block = b
			}
		}
		if block < 0 && background {
			for b := int64(0); b < w.blocks; b++ {
				if !w.done[b] && !w.claimed[b] {
					block = b
					break
				}
			}
		}
		if block >= 0 {
			w.claimed[block] = true
			delete(w.pending, block)
			blockCtx := ctx
			if !background {
				var cancel context.CancelCauseFunc
				blockCtx, cancel = context.WithCancelCause(ctx)
				w.active[block] = cancel
			}
			w.mu.Unlock()
			return block, blockCtx, true
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
	w.done[block] = true
	w.complete++
	delete(w.claimed, block)
	if cancel := w.active[block]; cancel != nil {
		cancel(nil)
		delete(w.active, block)
	}
	w.signalLocked()
	w.mu.Unlock()
}

func (w *rangeWork) abandon(block int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.claimed, block)
	delete(w.active, block)
	// A new reader may have arrived while the canceled request unwound.
	if w.readers[block] > 0 {
		w.pending[block] = true
	}
	w.signalLocked()
}

func (s *Server) publishRanges(ctx context.Context, first *http.Response, path string, v video, flight *flight) error {
	f, err := os.CreateTemp(s.cfg.tempDir(), "download-*.part")
	if err != nil {
		return err
	}
	work := &rangeWork{claimed: map[int64]bool{0: true}, done: make(map[int64]bool), pending: make(map[int64]bool), changed: make(chan struct{}), blocks: (v.size + rangeBlockSize - 1) / rangeBlockSize}
	work.readers = make(map[int64]int)
	work.active = make(map[int64]context.CancelCauseFunc)
	sp := &spool{file: f, changed: make(chan struct{}), refs: 1, demand: work.demand}
	flight.spool = sp
	close(flight.streaming)
	flight.progress.setStage("range_download")
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stopFirst := context.AfterFunc(ctx, func() { first.Body.Close() })
	defer stopFirst()
	// Pin all subsequent segments to the first response's validated endpoint.
	if first.Request != nil {
		selected, parseErr := s.parseResolved(first.Request)
		if parseErr != nil {
			return parseErr
		}
		v.host, v.path, v.query = selected.host, selected.path, selected.query
	}
	etag := first.Header.Get("ETag")
	if strings.HasPrefix(etag, "W/") {
		etag = ""
	}
	readBlock := func(ctx context.Context, block int64, initial *http.Response) error {
		start, end := block*rangeBlockSize, min((block+1)*rangeBlockSize, v.size)
		resp := initial
		attempts := new(resourceAttempts)
		if resp != nil {
			if body, ok := resp.Body.(*resourceBody); ok {
				attempts = body.attempts
			}
		}
		var last error
		for attempt := 0; attempt < 4; attempt++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if resp == nil {
				req, err := s.routeRequest(ctx, v)
				if err != nil {
					return applog.SafeError(err)
				}
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
				if etag != "" {
					req.Header.Set("If-Match", etag)
				}
				candidates := s.candidates(req, attempts)
				if candidates.next >= max(2, len(candidates.transports)) {
					break
				}
				resp, err = s.videoResponse(req, v, attempts)
				if err != nil {
					last = applog.SafeError(err)
					continue
				}
			}
			if err := validateRangeResponse(resp, start, end, v.size); err != nil {
				resp.Body.Close()
				resp = nil
				last = err
				continue
			}
			if etag != "" && resp.Header.Get("ETag") != etag {
				resp.Body.Close()
				resp = nil
				last = errors.New("upstream entity changed between ranges")
				continue
			}
			data, err := io.ReadAll(io.LimitReader(contextReader{ctx, progressReader{upstreamReader{resp.Body}, flight.progress}}, end-start+1))
			resp.Body.Close()
			resp = nil
			if err != nil || int64(len(data)) != end-start {
				last = applog.SafeError(err)
				if last == nil {
					last = io.ErrUnexpectedEOF
				}
				continue
			}
			if _, err := sp.WriteAt(data, start); err != nil {
				return err
			}
			work.finish(block)
			return nil
		}
		return fmt.Errorf("%w: range %d-%d: %w", errUpstreamDownload, start, end-1, last)
	}
	results := make(chan struct{}, 2)
	worker := func(background bool) {
		defer func() { results <- struct{}{} }()
		if background {
			if err := readBlock(ctx, 0, first); err != nil {
				cancel(err)
				return
			}
		}
		for {
			block, blockCtx, ok := work.next(ctx, background)
			if !ok {
				return
			}
			if err := readBlock(blockCtx, block, nil); err != nil {
				if errors.Is(context.Cause(blockCtx), errRangeAbandoned) && ctx.Err() == nil {
					work.abandon(block)
					continue
				}
				cancel(err)
				return
			}
		}
	}
	go worker(true)
	go worker(false)
	<-results
	<-results
	// The first failure cancels its peer; that internal cancellation is not
	// another root cause. Parent shutdown/deadlines retain their own cause.
	err = context.Cause(ctx)
	if err != nil {
		return err
	}
	flight.progress.setStage("hash")
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := checkOpenFile(ctx, f, v); err != nil {
		return fmt.Errorf("%w: %w", errUpstreamDownload, err)
	}
	flight.log.Info("download_verified", "bytes", v.size, "size_ok", true, "checksum_ok", true)
	return s.publishSpool(ctx, f, path, v, flight)
}
