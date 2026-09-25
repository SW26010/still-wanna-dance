package cacheproxy

import (
	"context"
	"crypto/md5" // The observed protocol supplies MD5; this is integrity checking, not authentication.
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"stepstash/internal/applog"
)

var errBusy = errors.New("download capacity reached")
var errInvalidCache = errors.New("invalid cached video")
var errUpstreamDownload = errors.New("upstream download failed")

func (s *Server) obtain(ctx context.Context, v video) (*flight, *spoolReader, error) {
	return s.obtainMode(ctx, v, false)
}

func (s *Server) obtainMode(ctx context.Context, v video, background bool) (*flight, *spoolReader, error) {
	ctx = applog.WithTrace(ctx)
	log := s.cfg.Logger.With("trace_id", applog.TraceID(ctx), "resource_key", v.key, "background", background)
	waitStart := time.Now()
	waitLogged := false
	s.mu.Lock()
	// Background capacity belongs to the actual flight, even if its caller
	// cancels. Keep one slot for playback when the configured limit permits it.
	limit := max(1, min(2, s.cfg.MaxDownloads-1))
	for {
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return nil, nil, err
		}
		if s.closed {
			s.mu.Unlock()
			return nil, nil, context.Canceled
		}
		if s.flights[v.key] != nil || (len(s.slots) < cap(s.slots) && (!background || s.background < limit)) {
			break
		}
		if !background {
			s.mu.Unlock()
			log.Warn("cache_capacity_rejected")
			return nil, nil, errBusy
		}
		changed := s.capacityChanged
		s.mu.Unlock()
		if !waitLogged {
			log.Info("cache_capacity_wait")
			waitLogged = true
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-s.ctx.Done():
			return nil, nil, context.Canceled
		case <-changed:
		}
		s.mu.Lock()
	}
	f := s.flights[v.key]
	if f == nil {
		// Bound all workers (including validation); callers sharing a key do not consume another slot.
		select {
		case s.slots <- struct{}{}:
		default:
			s.mu.Unlock()
			return nil, nil, errBusy
		}
		f = &flight{done: make(chan struct{}), streaming: make(chan struct{}), id: s.flightSequence.Add(1)}
		f.log = log.With("flight_id", f.id, "host", v.host)
		s.flights[v.key] = f
		if background {
			s.background++
		}
		close(s.capacityChanged)
		s.capacityChanged = make(chan struct{})
		s.wg.Add(1)
		s.pinVideo(v)
		go func() {
			defer s.wg.Done()
			defer s.releaseVideo(v)
			workerCtx, cancel := context.WithTimeout(s.ctx, s.cfg.DownloadTimeout)
			defer cancel()
			f.progress = startProgress(f.log, v.size, 15*time.Second)
			f.path, f.source, f.err = s.prepare(workerCtx, v, f)
			f.progress.finish(f.err)
			if f.err != nil {
				f.log.Error("cache_task_failed", "key", v.key, "error", applog.SafeError(f.err))
			}
			if f.spool != nil {
				f.spool.finish(f.err)
			}
			s.mu.Lock()
			delete(s.flights, v.key)
			<-s.slots
			if background {
				s.background--
			}
			close(s.capacityChanged)
			s.capacityChanged = make(chan struct{})
			close(f.done)
			s.mu.Unlock()
			if f.spool != nil {
				f.spool.release()
			}
		}()
	}
	s.mu.Unlock()
	log.Info("cache_task_attached", "flight_id", f.id, "wait_ms", time.Since(waitStart).Milliseconds())
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-f.done:
		return f, nil, f.err
	case <-f.streaming:
		if reader := f.spool.reader(ctx, v.size); reader != nil {
			return f, reader, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-f.done:
			return f, nil, f.err
		}
	}
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func checkFile(ctx context.Context, path string, v video) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return checkOpenFile(ctx, f, v)
}

func checkOpenFile(ctx context.Context, f *os.File, v video) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != v.size {
		return fmt.Errorf("%w: cached size mismatch", errInvalidCache)
	}
	h := md5.New()
	if _, err := io.Copy(h, contextReader{ctx, f}); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != v.checksum {
		return fmt.Errorf("%w: cached checksum mismatch", errInvalidCache)
	}
	return nil
}

func (s *Server) prepare(ctx context.Context, v video, flight *flight) (string, string, error) {
	path := s.cfg.videoFile(v.key)
	if err := checkFile(ctx, path, v); err == nil {
		flight.progress.setStage("index")
		if err := s.recordVideo(ctx, v); err != nil {
			return "", "", err
		}
		return path, "HIT", nil
	} else if ctx.Err() != nil {
		return "", "", ctx.Err()
	} else if !errors.Is(err, os.ErrNotExist) {
		flight.log.Warn("cache_invalid", "key", v.key, "error", err)
		if err := os.Remove(path); err != nil {
			return "", "", err
		}
	}
	flight.log.Info("download_started", "key", v.key, "size", v.size)
	flight.progress.setStage("upstream_headers")
	resp, selectedHost, err := s.openUpstream(ctx, v)
	if err != nil {
		return "", "", applog.SafeError(err)
	}
	defer resp.Body.Close()
	flight.log.Info("upstream_response", "host", selectedHost, "status", resp.StatusCode, "content_length", resp.ContentLength)
	started := time.Now()
	if err := s.publish(ctx, resp.Body, path, v, flight); err != nil {
		if errors.Is(err, errUpstreamDownload) && !errors.Is(err, context.Canceled) {
			s.noteRoute(selectedHost, time.Since(started), true)
		}
		return "", "", err
	}
	s.confirmRoute(selectedHost, time.Duration(float64(time.Since(started))*float64(min(v.size, 64<<10))/float64(v.size)))
	flight.progress.setStage("index")
	if err := s.recordVideo(ctx, v); err != nil {
		return "", "", err
	}
	flight.log.Info("download_published", "key", v.key, "size", v.size)
	return path, "MISS", nil
}

func (s *Server) publish(ctx context.Context, src io.Reader, path string, v video, flight *flight) error {
	f, err := os.CreateTemp(s.cfg.tempDir(), "download-*.part")
	if err != nil {
		return err
	}
	sp := &spool{file: f, changed: make(chan struct{}), refs: 1}
	flight.spool = sp
	close(flight.streaming)
	h := md5.New()
	flight.progress.setStage("download_and_hash")
	n, err := io.Copy(io.MultiWriter(sp, h), io.LimitReader(contextReader{ctx, progressReader{upstreamReader{src}, flight.progress}}, v.size+1))
	if err != nil {
		return fmt.Errorf("download read/write: %w", err)
	}
	if n != v.size || hex.EncodeToString(h.Sum(nil)) != v.checksum {
		flight.log.Warn("download_integrity_failed", "bytes", n, "expected_bytes", v.size, "size_ok", n == v.size, "checksum_ok", hex.EncodeToString(h.Sum(nil)) == v.checksum)
		return fmt.Errorf("%w: download integrity mismatch", errUpstreamDownload)
	}
	flight.log.Info("download_verified", "bytes", n, "size_ok", true, "checksum_ok", true)
	flight.progress.setStage("publish")
	if err := ctx.Err(); err != nil {
		return err
	}
	// Streaming readers retain the spool handle. Copy verified bytes to a closed
	// publication file so Windows can rename it without invalidating active reads.
	final, err := os.CreateTemp(s.cfg.tempDir(), "download-publish-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(final.Name())
	defer final.Close()
	if _, err := io.Copy(final, contextReader{ctx, io.NewSectionReader(f, 0, v.size)}); err != nil {
		return err
	}
	if err := final.Sync(); err != nil {
		return err
	}
	if err := final.Close(); err != nil {
		return err
	}
	return os.Rename(final.Name(), path)
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
