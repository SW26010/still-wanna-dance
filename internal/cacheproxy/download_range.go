package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/blocksched"
)

func (s *Server) readRange(ctx context.Context, flight *flight, block int64, progress *blocksched.Progress) error {
	download := flight.ranges
	s.retentionMu.Lock()
	v := download.video
	s.retentionMu.Unlock()
	sp := flight.spool
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
	start := block * s.cfg.BlockSize
	end := start + blockBytes(v.size, block, s.cfg.BlockSize)
	// Redirect progress belongs to this block; other workers keep their
	// own endpoint and candidate cursor.
	blockVideo := v
	attempts := &resourceAttempts{deadline: func() time.Time {
		return s.scheduler.TaskDeadline(downloadTaskKey(v.key, v.size, block))
	}}
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		var err error
		var data []byte
		exhausted := false
		received := false
		bodyStarted := false
		err = func() error {
			req, err := s.routeRequest(ctx, blockVideo)
			if err != nil {
				return err
			}
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
			s.retentionMu.Lock()
			if videoAddress(download.video) == videoAddress(blockVideo) {
				if etag := download.strongETag(); etag != "" {
					req.Header.Set("If-Match", etag)
				}
			}
			s.retentionMu.Unlock()
			candidates := s.candidates(req, attempts)
			if candidates.done {
				exhausted = true
				return last
			}
			resp, err := s.videoResponse(req, v, attempts)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			body := resp.Body
			stop := context.AfterFunc(ctx, func() { body.Close() })
			defer stop()
			if resp.Request != nil {
				selected, err := s.parse(resp.Request)
				if err != nil || selected.key != v.key || selected.size != v.size {
					return errors.New("invalid final range endpoint")
				}
				blockVideo.host, blockVideo.path, blockVideo.query = selected.host, selected.path, selected.query
			}
			received = true
			flight.log.Debug("upstream_response", "upstream_host", blockVideo.host, "status", resp.StatusCode, "content_length", resp.ContentLength)
			if err := validateRangeResponse(resp, start, end, v.size); err != nil {
				if errors.Is(err, errRangeUnsupported) {
					flight.log.Warn("range_unsupported", "upstream_host", blockVideo.host, "status", resp.StatusCode, "block", block)
				}
				return err
			}
			s.retentionMu.Lock()
			entityErr := download.acceptEntity(blockVideo, resp.Header.Get("ETag"))
			s.retentionMu.Unlock()
			if entityErr != nil {
				if errors.Is(entityErr, errEntityAddressChanged) {
					s.forgetLocalResource(v.key, download)
				}
				return entityErr
			}
			flight.progress.setHost(blockVideo.host)
			bodyStarted = true
			if flight.discard {
				// Stream into io.Discard's small reusable buffer; never allocate
				// a block-sized buffer, spool, or whole-video checksum state.
				n, err := io.Copy(io.Discard, io.LimitReader(contextReader{ctx, progressReader{upstreamReader{body}, flight.progress}}, end-start+1))
				if err != nil {
					return err
				}
				if n < end-start {
					return io.ErrUnexpectedEOF
				}
				if n > end-start {
					return errors.New("oversized range response")
				}
				return nil
			}
			if *buffer == nil {
				*buffer = s.rangeBuffer()
			}
			data, err = readRangeBlock(contextReader{ctx, progressReader{upstreamReader{body}, flight.progress}}, (*buffer)[:end-start+1])
			if err != nil {
				return err
			}
			if int64(len(data)) < end-start {
				return io.ErrUnexpectedEOF
			}
			if int64(len(data)) > end-start {
				return errors.New("oversized range response")
			}
			return nil
		}()
		if exhausted {
			break
		}
		if err != nil && ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if err != nil {
			// Only this response's body/validation errors belong to blockVideo.
			// videoResponse already records transport errors on the actual target;
			// a failed redirect must not authorize downgrading its healthy origin.
			if received {
				req, requestErr := s.routeRequest(ctx, blockVideo)
				if requestErr == nil {
					candidate := s.candidates(req, attempts)
					candidate.err = err
					candidate.attempts.RetrySingle = bodyStarted
				}
			}
			last = applog.SafeError(err)
			if !received || errors.Is(err, errTooManyVideoRedirects) || errors.Is(err, errEntityAddressChanged) {
				break
			}
			continue
		}
		if !flight.discard {
			if _, err := sp.WriteAt((*buffer)[:end-start], start); err != nil {
				return err
			}
			s.notifyBlockStored()
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
		return make([]byte, s.cfg.BlockSize+1)
	}
}

// Mark errors at the network reader, before io.Copy combines source reads and
// local writes. Filesystem failures must never affect upstream route health.
type upstreamReader struct{ io.Reader }

func (r upstreamReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err != nil && err != io.EOF {
		err = fmt.Errorf("%w: %w", errUpstreamDownload, err)
	}
	return n, err
}
