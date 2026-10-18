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

func (s *Server) readScheduledRange(ctx context.Context, block int64, initial *http.Response, flight *flight, progress *blocksched.Progress) error {
	if initial != nil {
		defer initial.Body.Close()
	}
	download := flight.ranges
	v := download.video
	sp := flight.spool
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
	blockVideo := v
	resp := initial
	attempts := new(resourceAttempts)
	if resp != nil {
		if body, ok := resp.Body.(*resourceBody); ok {
			attempts = body.attempts
		}
	}
	var last error
	for {
		if err := ctx.Err(); err != nil {
			if isScheduledPause(ctx) {
				return blocksched.ErrPaused
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
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
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
			if err := validateRangeResponse(resp, start, end, v.size); err != nil {
				return err
			}
			if etag != "" && resp.Header.Get("ETag") != etag {
				return errors.New("upstream entity changed between ranges")
			}
			if flight.discard {
				// Stream into io.Discard's small reusable buffer; never allocate
				// a block-sized buffer, spool, or whole-video checksum state.
				n, err := io.Copy(io.Discard, io.LimitReader(contextReader{transferCtx, progressReader{upstreamReader{body}, flight.progress}}, end-start+1))
				if err != nil {
					return err
				}
				if n != end-start {
					return io.ErrUnexpectedEOF
				}
				return nil
			}
			if *buffer == nil {
				*buffer = s.rangeBuffer()
			}
			data, err = readRangeBlock(contextReader{transferCtx, progressReader{upstreamReader{body}, flight.progress}}, (*buffer)[:end-start+1])
			if err != nil {
				return err
			}
			if int64(len(data)) != end-start {
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
			return blocksched.ErrPaused
		}
		if err != nil {
			last = applog.SafeError(err)
			continue
		}
		if !flight.discard {
			if _, err := sp.WriteAt((*buffer)[:end-start], start); err != nil {
				return err
			}
		}
		download.finish(block)
		progress.Ready()
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
