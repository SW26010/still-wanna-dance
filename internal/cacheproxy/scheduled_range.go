package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/blocksched"
)

func isScheduledPause(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), blocksched.ErrPaused) || errors.Is(context.Cause(ctx), errPreempted)
}

type schedulerProgressReader struct {
	io.Reader
	progress *blocksched.Progress
}

func (r schedulerProgressReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.progress.Add(int64(n))
	return n, err
}
func (s *Server) readScheduledRange(ctx context.Context, block int64, initial *http.Response, flight *flight, progress *blocksched.Progress) error {
	if initial != nil {
		defer initial.Body.Close()
	}
	download := flight.ranges
	v := download.video
	f, sp := download.storage.file, flight.spool
	etag := download.etag
	if strings.HasPrefix(etag, "W/") {
		etag = ""
	}
	var bytes []byte
	defer func() {
		if bytes != nil {
			select {
			case s.rangeBuffers <- bytes:
			default:
			}
		}
	}()
	buffer := &bytes
	start, end := block*rangeBlockSize, min((block+1)*rangeBlockSize, v.size)
	// Redirect progress belongs to this block; other workers keep their
	// own endpoint and candidate cursor.
	state := download.take(block)
	blockVideo := state.video
	resp := initial
	attempts := state.attempts
	if resp != nil {
		if body, ok := resp.Body.(*resourceBody); ok {
			attempts = body.attempts
		}
	}
	last, received := state.last, state.received
	if received > 0 {
		if *buffer == nil {
			*buffer = s.rangeBuffer()
		}
		if _, err := f.ReadAt((*buffer)[:received], start); err != nil {
			return err
		}
	}
	resume := func() {
		if r := attempts.resume(); r != nil {
			if selected, err := s.parse(r); err == nil && selected.key == v.key && selected.size == v.size {
				blockVideo.host, blockVideo.path, blockVideo.query = selected.host, selected.path, selected.query
			}
		}
	}
	failures := state.failures
	pause := func() error {
		resume()
		state.video, state.received, state.failures, state.last, state.attempts = blockVideo, received, failures, last, attempts
		if err := download.save(block, state, *buffer); err != nil {
			return err
		}
		return blocksched.ErrPaused
	}
	for failures < 4 {
		if err := ctx.Err(); err != nil {
			if isScheduledPause(ctx) {
				return pause()
			}
			return err
		}
		transferCtx := ctx
		var err error
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
				*buffer = s.rangeBuffer()
			}
			data, err = readRangeBlock(contextReader{transferCtx, schedulerProgressReader{progressReader{upstreamReader{body}, flight.progress}, progress}}, (*buffer)[received:end-start+1])
			if err != nil {
				return err
			}
			if received+int64(len(data)) != end-start {
				return io.ErrUnexpectedEOF
			}
			return nil
		}()
		preempted := isScheduledPause(ctx)
		resp = nil
		if exhausted {
			break
		}
		if err != nil && preempted {
			received += int64(len(data))
			// A canceled response with all advertised bytes but without EOF
			// still needs normal oversize validation; do not publish it early.
			if received >= end-start {
				progress.SetSize(end - start)
				progress.Invalidate()
				received = 0
			}
			if preempted {
				return pause()
			}
		}
		if err != nil {
			progress.SetSize(end - start)
			progress.Invalidate()
			last = applog.SafeError(err)
			received = 0
			failures++
			continue
		}
		if _, err := sp.WriteAt((*buffer)[:end-start], start); err != nil {
			return err
		}
		download.finish(block)
		return nil
	}
	return fmt.Errorf("%w: range %d-%d: %w", errUpstreamDownload, start, end-1, last)
}

func (s *Server) rangeBuffer() []byte {
	select {
	case buffer := <-s.rangeBuffers:
		return buffer
	default:
		return make([]byte, rangeBlockSize+1)
	}
}
