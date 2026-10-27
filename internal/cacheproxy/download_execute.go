package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"still-wanna-dance/internal/blocksched"
)

// downloadExecutor performs admitted work using the session's existing resource
// state. It owns response handling and cleanup, not demands or task lifetimes.
type downloadExecutor struct {
	s *Server
	v video
	f *flight

	waitForBlock     func(int64) func()
	bindReads        func(int64)
	initializeRanges func(*localResource)
}

// run normally delivers one range. An initial 200 keeps this execution for the
// whole sequential response. All bodies and writes finish before run returns.
func (e *downloadExecutor) run(ctx context.Context, block int64, p *blocksched.Progress) error {
	// Each upstream attempt owns its timeout; intentional scheduler waits and
	// retries do not consume a shared execution timer. Parent cancellation and
	// the demand/task lifetime still bound all attempts.
	// The global block admission covers retries/redirects. HTTP attempts only
	// track their lifecycle; no lower-level hard quota delays blocked reads.
	var initial *http.Response
	if e.f.ranges == nil || e.f.ranges.sequential {
		resp, host, err := e.s.openUpstream(ctx, e.v, block, new(resourceAttempts))
		if err != nil {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			return fmt.Errorf("%w: %w", errUpstreamDownload, err)
		}
		defer resp.Body.Close()
		if resp.Request != nil && resp.Request.URL != nil {
			host = resp.Request.URL.Hostname()
			e.f.progress.setHost(host)
		}
		e.f.log.Info("upstream_response", "upstream_host", host, "status", resp.StatusCode, "content_length", resp.ContentLength)
		if resp.StatusCode != http.StatusPartialContent {
			if e.bindReads != nil {
				e.bindReads(block)
			}
			e.s.mu.Lock()
			e.f.sequentialCtx = ctx
			e.f.sequentialDemand = func(int64) func() { return e.waitForBlock(block) }
			e.s.mu.Unlock()
			e.f.log.Info("range_unavailable", "upstream_host", host, "fallback", "sequential")
			stopBody := context.AfterFunc(ctx, func() { resp.Body.Close() })
			defer stopBody()
			if e.f.discard {
				_, err := io.Copy(io.Discard, contextReader{ctx, progressReader{upstreamReader{resp.Body}, e.f.progress}})
				return err
			}
			err := e.readSequential(ctx, resp.Body)
			// Sequential bodies have no safe partial restart. End this flight;
			// its next owner opens a new body after cleanup. Preserve storage
			// and integrity errors that happen to coincide with cancellation.
			if ctx.Err() != nil && errors.Is(err, context.Canceled) {
				e.s.preemptFlight(e.f, nil)
				err = errPreempted
			}
			return err
		}
		v := e.v
		if resp.Request != nil {
			selected, err := e.s.parseResolved(resp.Request)
			if err != nil {
				return err
			}
			v.host, v.path, v.query = selected.host, selected.path, selected.query
		}
		var d *localResource
		if e.f.discard {
			d = &localResource{blockSize: e.s.cfg.BlockSize, video: resourceVideo(v), etag: resp.Header.Get("ETag"),
				done: make(map[int64]bool)}
		} else if e.f.ranges != nil {
			d = e.f.ranges
			d.sequential = false
			d.etag = resp.Header.Get("ETag")
		} else {
			var err error
			d, err = e.s.newLocalResource(v, resp, e.f.log)
			if err != nil {
				return err
			}
		}
		// Publish the existing resource/spool and initialization signals before
		// the coordinator can admit other blocks; this block is not ready yet.
		e.initializeRanges(d)
		initial = resp
	}
	return e.readRange(ctx, block, initial, p)
}

// A sequential response uses the same complete-block store as Range downloads.
// Incomplete blocks remain in memory and are discarded when execution stops.
func (e *downloadExecutor) readSequential(ctx context.Context, src io.Reader) error {
	s, v, flight := e.s, e.v, e.f
	if flight.ranges == nil {
		d, err := s.newLocalResource(v, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}, flight.log)
		if err != nil {
			return err
		}
		flight.ranges = d
	}
	sp := flight.spool
	if sp == nil {
		sp = flight.ranges.makeSpool(flight.sequentialDemand)
		flight.spool = sp
		close(flight.streaming)
	}
	flight.progress.setStage("download")
	reader := contextReader{ctx, progressReader{upstreamReader{src}, flight.progress}}
	buffer := s.rangeBuffer()
	defer func() {
		select {
		case s.rangeBuffers <- buffer:
		default:
		}
	}()
	for block := int64(0); block < blockCount(v.size, s.cfg.BlockSize); block++ {
		size := blockBytes(v.size, block, s.cfg.BlockSize)
		if _, err := io.ReadFull(reader, buffer[:size]); err != nil {
			return fmt.Errorf("%w: sequential block: %w", errUpstreamDownload, err)
		}
		// Require EOF before exposing the final block; an oversized response is invalid.
		if block == blockCount(v.size, s.cfg.BlockSize)-1 {
			if _, err := io.ReadFull(reader, buffer[size:size+1]); err != io.EOF {
				if err == nil {
					err = errors.New("oversized sequential response")
				}
				return fmt.Errorf("%w: %w", errUpstreamDownload, err)
			}
		}
		if _, err := sp.WriteAt(buffer[:size], block*s.cfg.BlockSize); err != nil {
			return err
		}
		s.notifyBlockStored()
	}
	return nil
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
