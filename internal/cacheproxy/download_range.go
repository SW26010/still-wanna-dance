package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"still-wanna-dance/internal/applog"
)

func (s *Server) readRange(ctx context.Context, flight *flight, block int64, ready func(), needBy func() time.Time) error {
	download := flight.ranges
	s.retentionMu.Lock()
	v := download.video
	s.retentionMu.Unlock()
	return s.readRangeAt(ctx, flight, block, v, ready, &resourceAttempts{block: useBlockID{v.key, block}, needBy: needBy, fallbackDeadline: func() time.Time { return sourceDeadline(needBy()) }})
}

// One pass over the eligible routes for a validated URL.
func (s *Server) readRangeAt(ctx context.Context, flight *flight, block int64, v video, ready func(), attempts *resourceAttempts) error {
	download := flight.ranges
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
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		received := false
		bodyStarted := false
		var sharedUntil time.Time
		err := func() (result error) {
			req, err := s.routeRequest(ctx, blockVideo)
			if err != nil {
				return err
			}
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
			if etag := download.etagFor(blockVideo); etag != "" {
				req.Header.Set("If-Match", etag)
			}
			resp, err := s.videoResponse(req, v, attempts)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			body := resp.Body
			observation := body.(*resourceBody).observation
			defer func() {
				switch {
				case result == nil:
					observation.finish(end-start, nil)
				case bodyStarted && (errors.Is(result, errUpstreamDownload) || errors.Is(result, io.ErrUnexpectedEOF)):
					sharedUntil = observation.finish(0, result)
				}
			}()
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
			flight.log.Debug("已取得上游响应", "event_name", "upstream_response", "upstream_host", blockVideo.host, "status", resp.StatusCode, "content_length", resp.ContentLength)
			if err := validateRangeResponse(resp, start, end, v.size); err != nil {
				switch resp.StatusCode {
				case http.StatusForbidden, http.StatusNotFound, http.StatusGone:
					if attempts.persistent {
						attempts.rejectedURL = blockVideo.requestURL()
					}
				}
				if errors.Is(err, errRangeUnsupported) {
					flight.log.Warn("上游不支持所需 Range", "event_name", "range_unsupported", "upstream_host", blockVideo.host, "status", resp.StatusCode, "block", block)
				}
				return err
			}
			etag := resp.Header.Get("ETag")
			if bound := download.etagFor(blockVideo); bound != "" && bound != etag {
				return errRangeEntityChanged
			}
			flight.progress.setHost(blockVideo.host)
			bodyStarted = true
			reader := contextReader{ctx, progressReader{upstreamReader{body}, flight.progress}}
			var n int64
			if flight.discard {
				// Stream into io.Discard's small reusable buffer; never allocate
				// a block-sized buffer, spool, or whole-video checksum state.
				n, err = io.Copy(io.Discard, io.LimitReader(reader, end-start+1))
			} else {
				if *buffer == nil {
					*buffer = s.rangeBuffer()
				}
				var data []byte
				data, err = readRangeBlock(reader, (*buffer)[:end-start+1])
				n = int64(len(data))
			}
			if err != nil {
				return err
			}
			if n < end-start {
				return io.ErrUnexpectedEOF
			}
			if n > end-start {
				return errors.New("oversized range response")
			}
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			// Another block may have bound this URL while we read the body.
			// Incomplete responses must never pin a validator for later retries.
			s.retentionMu.Lock()
			entityErr := download.acceptEntity(blockVideo, etag)
			s.retentionMu.Unlock()
			return entityErr
		}()
		if err != nil && ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if err == errReselectSource {
			return err
		}
		if err != nil {
			// Only this response's body/validation errors belong to blockVideo.
			// videoResponse already records transport errors on the actual target;
			// a failed redirect must not authorize downgrading its healthy origin.
			if received && attempts.rejectedURL == "" {
				req, requestErr := s.routeRequest(ctx, blockVideo)
				if requestErr == nil {
					candidate := s.candidates(req, attempts)
					attempts.failedPath(candidate, sharedUntil)
					candidate.err = err
					candidate.attempts.RetrySingle = bodyStarted && !attempts.persistent
				}
			}
			last = applog.SafeError(err)
			if !received || attempts.rejectedURL != "" {
				break
			}
			continue
		}
		if !flight.discard {
			if _, err := sp.WriteAt((*buffer)[:end-start], start); err != nil {
				return err
			}
			flight.addresses.mu.Lock()
			flight.addresses.recordLocked(blockVideo, true)
			flight.addresses.mu.Unlock()
			s.notifyBlockStored(v.key)
		}
		download.finish(block)
		ready()
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
