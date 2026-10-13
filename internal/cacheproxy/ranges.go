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
var errRangeYielded = errors.New("range yielded to seek")

// readRangeBlock fills a worker-owned buffer, including one extra byte to detect
// oversized responses. Preserve non-EOF errors even when returned with data.
func readRangeBlock(r io.Reader, buf []byte) ([]byte, error) {
	n := 0
	for n < len(buf) {
		count, err := r.Read(buf[n:])
		n += count
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return buf[:n], err
		}
	}
	return buf[:n], nil
}

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

func (s *Server) publishRanges(ctx context.Context, first *http.Response, path string, v video, flight *flight) error {
	f, err := os.CreateTemp(s.cfg.tempDir(), "download-*.part")
	if err != nil {
		return err
	}
	work := newRangeWork(v.size)
	// Cold playback gets its first block before speculative fan-out. Prefetch
	// can fill parallel ranges immediately, and a tail reader still bypasses 0.
	work.startup = !flight.background
	sp := &spool{file: f, changed: make(chan struct{}), refs: 1, demand: work.demand, track: work.trackReader}
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
	readBlock := func(ctx context.Context, block int64, initial *http.Response, buffer *[]byte) error {
		start, end := block*rangeBlockSize, min((block+1)*rangeBlockSize, v.size)
		// Redirect progress belongs to this block; other workers keep their
		// own endpoint and candidate cursor.
		blockVideo := v
		resp := initial
		attempts := new(resourceAttempts)
		if resp != nil {
			if body, ok := resp.Body.(*resourceBody); ok {
				attempts = body.attempts
			}
		}
		var last error
		var received int64
		resume := func() {
			if r := attempts.resume(); r != nil {
				if selected, err := s.parse(r); err == nil && selected.key == v.key && selected.size == v.size {
					blockVideo.host, blockVideo.path, blockVideo.query = selected.host, selected.path, selected.query
				}
			}
		}
		for failures := 0; failures < 4; {
			if err := ctx.Err(); err != nil {
				return err
			}
			work.mu.Lock()
			wait := work.shouldWaitLocked(block)
			work.mu.Unlock()
			if resp != nil && wait {
				// An immediate tail Seek can arrive before the first worker runs.
				resp.Body.Close()
				resp = nil
				resume()
			}
			transferCtx, endTransfer, err := work.beginTransfer(ctx, block)
			if err != nil {
				return err
			}
			var data []byte
			exhausted := false
			err = func() error {
				if resp == nil {
					attempts.current = nil
					req, err := s.routeRequest(transferCtx, blockVideo)
					if err != nil {
						return err
					}
					req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start+received, end-1))
					if etag != "" {
						req.Header.Set("If-Match", etag)
					}
					candidates := s.candidates(req, attempts)
					if candidates.next >= max(2, len(candidates.transports)) {
						exhausted = true
						return last
					}
					resp, err = s.videoResponse(req, v, attempts)
					if err != nil {
						return err
					}
				}
				defer resp.Body.Close()
				body := resp.Body
				stop := context.AfterFunc(transferCtx, func() { body.Close() })
				defer stop()
				if resp.Request != nil {
					selected, err := s.parse(resp.Request)
					if err != nil || selected.key != v.key || selected.size != v.size {
						return errors.New("invalid final range endpoint")
					}
					blockVideo.host, blockVideo.path, blockVideo.query = selected.host, selected.path, selected.query
				}
				if err := validateRangeResponse(resp, start+received, end, v.size); err != nil {
					return err
				}
				if etag != "" && resp.Header.Get("ETag") != etag {
					return errors.New("upstream entity changed between ranges")
				}
				if *buffer == nil {
					*buffer = make([]byte, min(rangeBlockSize, v.size)+1)
				}
				data, err = readRangeBlock(contextReader{transferCtx, progressReader{upstreamReader{body}, flight.progress}}, (*buffer)[received:end-start+1])
				if err != nil {
					return err
				}
				if received+int64(len(data)) != end-start {
					return io.ErrUnexpectedEOF
				}
				return nil
			}()
			yielded := errors.Is(context.Cause(transferCtx), errRangeYielded)
			endTransfer()
			resp = nil
			if exhausted {
				break
			}
			if err != nil && yielded && ctx.Err() == nil {
				received += int64(len(data))
				// A canceled response with all advertised bytes but without EOF
				// still needs normal oversize validation; do not publish it early.
				if received >= end-start {
					received = 0
				}
				resume()
				flight.log.Debug("download_range_yielded", "block", block, "retained_bytes", received)
				continue
			}
			if err != nil {
				last = applog.SafeError(err)
				received = 0
				failures++
				continue
			}
			if _, err := sp.WriteAt((*buffer)[:end-start], start); err != nil {
				return err
			}
			work.finish(block)
			return nil
		}
		return fmt.Errorf("%w: range %d-%d: %w", errUpstreamDownload, start, end-1, last)
	}
	var workers sync.WaitGroup
	worker := func(background bool, initial *http.Response) {
		// Allocate only when this worker reads a valid response, then reuse for
		// later blocks and retries. WriteAt consumes the bytes before reuse.
		var buffer []byte
		if initial != nil {
			if err := readBlock(work.requestContext(ctx, 0), 0, initial, &buffer); err != nil {
				cancel(err)
				return
			}
		}
		for {
			block, blockCtx, ok := work.next(ctx, background)
			if !ok {
				return
			}
			if err := readBlock(blockCtx, block, nil, &buffer); err != nil {
				if errors.Is(context.Cause(blockCtx), errRangeAbandoned) && ctx.Err() == nil {
					work.abandon(block)
					continue
				}
				cancel(err)
				return
			}
		}
	}
	workers.Go(func() { worker(true, first) })
	for i := 1; i < min(rangeBackgroundWorkers, int(work.blocks)); i++ {
		workers.Go(func() { worker(true, nil) })
	}
	workers.Go(func() { worker(false, nil) })
	workers.Wait()
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
