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

	"still-wanna-dance/internal/applog"
)

var errBusy = errors.New("download capacity reached")
var errInvalidCache = errors.New("invalid cached video")
var errUpstreamDownload = errors.New("upstream download failed")

// File/session metadata is bounded independently of network concurrency. Queue
// and batch callers remain bounded upstream; all admitted blocks compete globally.
const maxResourceSessions = 32

func (s *Server) obtain(ctx context.Context, v video) (*flight, *spoolReader, error) {
	return s.obtainMode(ctx, v, false)
}

func (s *Server) obtainMode(ctx context.Context, v video, background bool) (*flight, *spoolReader, error) {
	return s.obtainDemand(ctx, v, background, !background)
}

func (s *Server) obtainDemand(ctx context.Context, v video, background, playback bool) (*flight, *spoolReader, error) {
	ctx = applog.WithTrace(ctx)
	demand, releaseDemand := s.addDemand(ctx, v, playback)
	transferred := false
	defer func() {
		if !transferred {
			releaseDemand()
		}
	}()
	log := s.cfg.Logger.With("trace_id", applog.TraceID(ctx), "resource_key", v.key, "background", background)
	waitStart := time.Now()
	waitLogged := false
	s.mu.Lock()
	for {
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return nil, nil, err
		}
		if s.closed {
			s.mu.Unlock()
			return nil, nil, context.Canceled
		}
		if s.storageFailure != nil {
			failure := s.storageFailure
			s.mu.Unlock()
			// Verified files remain readable even when new storage work is disabled.
			if _, err := s.verifiedFile(ctx, v); err == nil {
				f := &flight{path: s.cfg.videoFile(v.key), source: "HIT", done: make(chan struct{})}
				close(f.done)
				return f, nil, nil
			}
			return nil, nil, failure
		}
		f := s.flights[v.key]
		demand.waiting = f == nil || f.preempted
		if demand.queue && !s.queueContainsLocked(demand.song) {
			s.mu.Unlock()
			return nil, nil, context.Canceled
		}
		if f != nil && !f.preempted || (f == nil && len(s.slots) < cap(s.slots)) {
			break
		}
		changed := s.capacityChanged
		s.mu.Unlock()
		// A busy download engine must still serve verified local files. Keep
		// this disk work bounded independently of the network worker slots.
		if f, err := s.localHit(ctx, v); f != nil || err != nil {
			if err == nil && !background && v.songID != "" {
				err = s.recordSongVideo(ctx, v.songID, v)
			}
			return f, nil, err
		}
		s.mu.Lock()
		// Recheck after disk I/O: the requested session may have finished cleanup.
		if changed != s.capacityChanged {
			continue
		}
		preempting := false
		if f := s.flights[v.key]; f != nil && f.preempted {
			preempting = true
		}
		s.mu.Unlock()
		if !background && !preempting {
			log.Warn("cache_capacity_rejected")
			return nil, nil, errBusy
		}
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
	demand.waiting = false
	f := s.flights[v.key]
	if f != nil && f.size != v.size {
		s.mu.Unlock()
		return nil, nil, errors.New("shared resource size mismatch")
	}
	if paused := s.paused[v.key]; paused != nil && paused.video.size != v.size {
		s.mu.Unlock()
		return nil, nil, errors.New("paused resource size mismatch")
	}
	if f == nil {
		// Bound all workers (including validation); callers sharing a key do not consume another slot.
		select {
		case s.slots <- struct{}{}:
		default:
			s.mu.Unlock()
			return nil, nil, errBusy
		}
		f = &flight{size: v.size, done: make(chan struct{}), streaming: make(chan struct{}), id: s.flightSequence.Add(1)}
		f.ranges = s.paused[v.key]
		delete(s.paused, v.key)
		workerCtx, cancel := context.WithCancelCause(s.ctx)
		f.cancel, f.background = cancel, background
		f.log = log.With("flight_id", f.id, "host", v.host)
		f.progress = startProgress(f.log, v.size, 15*time.Second)
		f.monitorSongs = make(map[string]bool)
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
			defer cancel(nil)
			timedCtx, stop := context.WithTimeout(workerCtx, s.cfg.DownloadTimeout)
			defer stop()
			f.path, f.source, f.err = s.prepare(timedCtx, v, f)
			if f.err == nil {
				s.mu.Lock()
				f.completed = true
				s.mu.Unlock()
			}
			if errors.Is(context.Cause(workerCtx), errPreempted) && errors.Is(f.err, context.Canceled) {
				f.err = errPreempted
			}
			if errors.Is(f.err, errPreempted) {
				s.mu.Lock()
				f.preempted = true
				s.mu.Unlock()
			}
			f.progress.finish(f.err)
			if errors.Is(f.err, errPreempted) {
				f.log.Info("cache_task_canceled", "reason", "preempted")
			} else if f.err != nil {
				f.log.Error("cache_task_failed", "key", v.key, "error", applog.SafeError(f.err))
			}
			if f.spool != nil {
				f.spool.finish(f.err)
				f.spool.release()
			}
			s.mu.Lock()
			checkpoint := f.ranges
			if checkpoint != nil && errors.Is(f.err, errPreempted) && !s.closed && s.storageFailure == nil {
				if s.paused == nil {
					s.paused = make(map[string]*rangeDownload)
				}
				s.paused[v.key] = checkpoint
				f.log.Info("download_paused", "retained_bytes", checkpoint.bytes())
				checkpoint = nil // the engine now owns this reference
			}
			s.mu.Unlock()
			if checkpoint != nil {
				checkpoint.storage.release()
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
			releases := f.releases
			s.mu.Unlock()
			for _, release := range releases {
				release()
			}
		}()
	}
	if v.songID != "" {
		f.monitorSongs[v.songID] = true
	}
	if !background && v.songID != "" {
		s.attachPlaybackSongLocked(f, v)
	}
	if background {
		f.releases = append(f.releases, releaseDemand)
		transferred = true
	}
	s.mu.Unlock()
	log.Info("cache_task_attached", "flight_id", f.id, "wait_ms", time.Since(waitStart).Milliseconds())
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-f.done:
		if f.err != nil {
			return f, nil, f.err
		}
		return f, nil, s.waitPlaybackSong(ctx, f, v.songID)
	case <-f.streaming:
		if background {
			return f, nil, nil
		}
		if reader := f.spool.reader(ctx, v.size); reader != nil {
			reader.releaseDemand = releaseDemand
			transferred = true
			return f, reader, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-f.done:
			if f.err != nil {
				return f, nil, f.err
			}
			return f, nil, s.waitPlaybackSong(ctx, f, v.songID)
		}
	}
}

// Callers pin the version and register their lifetime before obtaining it.
// Invalid or missing files still need the normal, capacity-limited worker.
func (s *Server) localHit(ctx context.Context, v video) (*flight, error) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	if s.ctx.Err() != nil {
		return nil, context.Canceled
	}
	path := s.cfg.videoFile(v.key)
	if _, err := os.Stat(path); err != nil {
		return nil, ctx.Err()
	}
	if _, err := s.verifiedFile(ctx, v); err != nil {
		return nil, ctx.Err()
	}
	if err := s.recordVideo(ctx, v); err != nil {
		return nil, err
	}
	f := &flight{path: path, source: "HIT", done: make(chan struct{})}
	close(f.done)
	return f, nil
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
	if hex.EncodeToString(h.Sum(nil)) != v.key {
		return fmt.Errorf("%w: cached checksum mismatch", errInvalidCache)
	}
	return nil
}

func (s *Server) prepare(ctx context.Context, v video, flight *flight) (pathResult, source string, resultErr error) {
	defer func() { resultErr = s.storageError(resultErr) }()
	path := s.cfg.videoFile(v.key)
	if _, err := s.verifiedFile(ctx, v); err == nil {
		flight.progress.setStage("index")
		if err := s.recordVideo(ctx, v); err != nil {
			return "", "", err
		}
		return path, "HIT", nil
	} else if ctx.Err() != nil {
		return "", "", ctx.Err()
	} else if !errors.Is(err, os.ErrNotExist) {
		if !errors.Is(err, errInvalidCache) {
			return "", "", err
		}
		flight.log.Warn("cache_invalid", "key", v.key, "error", err)
		// The worker pin excludes retention deletion during filesystem I/O.
		err := os.Remove(path)
		s.retentionMu.Lock()
		if err == nil || os.IsNotExist(err) {
			s.retainVideoLocked(v.key, nil)
		}
		s.retentionMu.Unlock()
		if err != nil {
			return "", "", err
		}
	}
	if s.cfg.BeginResourceLoad != nil {
		release := s.cfg.BeginResourceLoad()
		defer release()
	}
	flight.log.Info("download_started", "key", v.key, "size", v.size)
	if flight.ranges != nil {
		retained := flight.ranges.bytes()
		flight.progress.mu.Lock()
		flight.progress.bytes = retained
		flight.progress.mu.Unlock()
		flight.progress.setHost(flight.ranges.video.host)
		flight.log.Info("download_resumed", "retained_bytes", retained)
	}
	publishErr := s.publishScheduled(ctx, path, v, flight)
	if err := publishErr; err != nil {
		return "", "", err
	}
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
	sp := &spool{file: f, storage: &spoolFile{file: f, refs: 1, log: flight.log}, changed: make(chan struct{}), refs: 1, demand: flight.sequentialDemand}
	flight.spool = sp
	close(flight.streaming)
	h := md5.New()
	flight.progress.setStage("download_and_hash")
	n, err := io.Copy(io.MultiWriter(sp, h), io.LimitReader(contextReader{ctx, progressReader{upstreamReader{src}, flight.progress}}, v.size+1))
	if err != nil {
		return fmt.Errorf("download read/write: %w", err)
	}
	if n != v.size || hex.EncodeToString(h.Sum(nil)) != v.key {
		flight.log.Warn("download_integrity_failed", "bytes", n, "expected_bytes", v.size, "size_ok", n == v.size, "checksum_ok", hex.EncodeToString(h.Sum(nil)) == v.key)
		return fmt.Errorf("%w: download integrity mismatch", errUpstreamDownload)
	}
	flight.log.Info("download_verified", "bytes", n, "size_ok", true, "checksum_ok", true)
	return s.publishSpool(ctx, f, path, v, flight)
}

func (s *Server) publishSpool(ctx context.Context, f *os.File, path string, v video, flight *flight) error {
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
	if n, err := io.Copy(final, contextReader{ctx, io.NewSectionReader(f, 0, v.size)}); err != nil {
		return err
	} else if n != v.size {
		return fmt.Errorf("%w: publication copy mismatch", errInvalidCache)
	}
	if err := final.Sync(); err != nil {
		return err
	}
	if err := final.Close(); err != nil {
		return err
	}
	info, err := os.Stat(final.Name())
	if err != nil {
		return err
	}
	// The worker owns a pin, so retention cannot reserve this resource while
	// the atomic publication runs. Only inventory bookkeeping needs the lock.
	if err := os.Rename(final.Name(), path); err != nil {
		return err
	}
	s.retentionMu.Lock()
	item := retainedVideo{path: path, key: v.key, size: info.Size(), recent: info.ModTime().UnixMilli(), modified: info.ModTime().UnixNano()}
	s.retainVideoLocked(v.key, &item)
	if s.cfg.MaxCacheBytes > 0 && s.retainedBytes > s.cfg.MaxCacheBytes {
		s.requestRetentionLocked()
	}
	s.retentionMu.Unlock()
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
