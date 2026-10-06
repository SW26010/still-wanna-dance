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
)

const rangeBlockSize int64 = 1 << 20

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
	changed          chan struct{}
	blocks, complete int64
}

func (w *rangeWork) signalLocked() { close(w.changed); w.changed = make(chan struct{}) }
func (w *rangeWork) demand(offset int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	block := offset / rangeBlockSize
	if block < 0 || block >= w.blocks || w.done[block] || w.claimed[block] || w.pending[block] {
		return
	}
	w.pending[block] = true
	w.signalLocked()
}
func (w *rangeWork) next(ctx context.Context, background bool) (int64, bool) {
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
			w.mu.Unlock()
			return block, true
		}
		finished, changed := w.complete == w.blocks, w.changed
		w.mu.Unlock()
		if finished {
			return 0, false
		}
		select {
		case <-ctx.Done():
			return 0, false
		case <-changed:
		}
	}
}
func (w *rangeWork) finish(block int64) {
	w.mu.Lock()
	w.done[block] = true
	w.complete++
	delete(w.claimed, block)
	w.signalLocked()
	w.mu.Unlock()
}

func (s *Server) publishRanges(ctx context.Context, first *http.Response, path string, v video, flight *flight) error {
	f, err := os.CreateTemp(s.cfg.tempDir(), "download-*.part")
	if err != nil {
		return err
	}
	work := &rangeWork{claimed: map[int64]bool{0: true}, done: make(map[int64]bool), pending: make(map[int64]bool), changed: make(chan struct{}), blocks: (v.size + rangeBlockSize - 1) / rangeBlockSize}
	sp := &spool{file: f, changed: make(chan struct{}), refs: 1, demand: work.demand}
	flight.spool = sp
	close(flight.streaming)
	flight.progress.setStage("range_download")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
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
	readBlock := func(block int64, initial *http.Response) error {
		start, end := block*rangeBlockSize, min((block+1)*rangeBlockSize, v.size)
		resp := initial
		var last error
		for attempt := 0; attempt < 2; attempt++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if resp == nil {
				req, err := s.routeRequest(ctx, v)
				if err != nil {
					return err
				}
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
				if etag != "" {
					req.Header.Set("If-Match", etag)
				}
				resp, err = s.videoResponse(req, v)
				if err != nil {
					last = err
					continue
				}
			}
			if err := validateRangeResponse(resp, start, end, v.size); err != nil {
				resp.Body.Close()
				return fmt.Errorf("%w: %w", errUpstreamDownload, err)
			}
			if etag != "" && resp.Header.Get("ETag") != etag {
				resp.Body.Close()
				return fmt.Errorf("%w: upstream entity changed between ranges", errUpstreamDownload)
			}
			data, err := io.ReadAll(io.LimitReader(contextReader{ctx, progressReader{upstreamReader{resp.Body}, flight.progress}}, end-start+1))
			resp.Body.Close()
			resp = nil
			if err != nil || int64(len(data)) != end-start {
				last = err
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
	results := make(chan error, 2)
	worker := func(background bool) {
		if background {
			if err := readBlock(0, first); err != nil {
				cancel()
				results <- err
				return
			}
		}
		for {
			block, ok := work.next(ctx, background)
			if !ok {
				results <- ctx.Err()
				return
			}
			if err := readBlock(block, nil); err != nil {
				cancel()
				results <- err
				return
			}
		}
	}
	go worker(true)
	go worker(false)
	err = errors.Join(<-results, <-results)
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
