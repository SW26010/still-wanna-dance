package cacheproxy

import (
	"context"
	"crypto/md5" // The observed protocol supplies MD5; this is integrity checking, not authentication.
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"still-wanna-dance/internal/applog"
)

var errBusy = errors.New("download capacity reached")
var errInvalidCache = errors.New("invalid cached video")
var errUpstreamDownload = errors.New("upstream download failed")

// Commit the admission cutoff under the same lock used to register/attach GETs.
// A plan snapshot may only retire the flight if no new source arrived meanwhile.
func (s *Server) preemptFlight(f *flight, changed <-chan struct{}) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if changed != nil && changed != s.capacityChanged {
		return false
	}
	f.preempted = true
	return true
}

func (s *Server) obtain(ctx context.Context, v video) (*flight, *spoolReader, error) {
	return s.obtainMode(ctx, v, false)
}

func (s *Server) obtainMode(ctx context.Context, v video, background bool) (*flight, *spoolReader, error) {
	return s.obtainDemand(ctx, v, background, !background)
}

func (s *Server) obtainDemand(ctx context.Context, v video, background, playback bool) (*flight, *spoolReader, error) {
	ctx = applog.WithTrace(ctx)
	demand, releaseDemand, err := s.addDemand(ctx, v, playback)
	if err != nil {
		return nil, nil, err
	}
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

		if demand.queue && !s.validQueueSourceLocked(demand) {
			s.mu.Unlock()
			return nil, nil, context.Canceled
		}
		// Lease expiry can cancel a sequential response before its coordinator
		// or executor commits retirement. Never attach to that canceled body,
		// even while Read/Close are still unwinding. Range blocks can resume.
		if f != nil && f.sequentialCtx != nil && f.sequentialCtx.Err() != nil {
			f.preempted = true
		}
		if f == nil || !f.preempted {
			break
		}
		changed := s.capacityChanged
		s.mu.Unlock()
		// A retiring resource session must still allow a verified local hit.
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
	if f != nil && f.size != v.size {
		s.mu.Unlock()
		return nil, nil, errors.New("shared resource size mismatch")
	}
	if f == nil {
		f = &flight{size: v.size, done: make(chan struct{}), streaming: make(chan struct{}), id: s.flightSequence.Add(1)}
		local, err := s.acquireLocalResource(v)
		if err != nil {
			s.mu.Unlock()
			return nil, nil, err
		}
		f.ranges = local
		workerCtx, cancel := context.WithCancelCause(s.ctx)
		f.cancel, f.background = cancel, background
		f.log = log.With("flight_id", f.id, "host", v.host)
		f.progress = startProgress(f.log, v.size, 15*time.Second)
		f.progress.observeBytes = s.cfg.ObserveDownloadBytes
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
			f.path, f.source, f.err = s.prepare(workerCtx, v, f)
			if f.err == nil {
				s.mu.Lock()
				f.completed = true
				s.mu.Unlock()
			}
			if errors.Is(context.Cause(workerCtx), errPreempted) && errors.Is(f.err, context.Canceled) {
				f.err = errPreempted
			}
			if errors.Is(f.err, errPreempted) {
				s.preemptFlight(f, nil)
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
			if local := f.ranges; local != nil {
				if f.err == nil || errors.Is(f.err, errInvalidCache) || local.bytes() == 0 {
					s.forgetLocalResource(v.key, local)
				}
				local.storage.release()
			}
			s.mu.Lock()
			delete(s.flights, v.key)
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
	checkCtx, stopCheck := context.WithTimeout(ctx, s.cfg.DownloadTimeout)
	defer stopCheck()
	if _, err := s.verifiedFile(checkCtx, v); err == nil {
		flight.progress.setStage("index")
		if err := s.recordVideo(checkCtx, v); err != nil {
			return "", "", err
		}
		return path, "HIT", nil
	} else if checkCtx.Err() != nil {
		return "", "", checkCtx.Err()
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
	stopCheck() // Waiting for a block's activation window has no execution timer.
	flight.log.Info("download_started", "key", v.key, "size", v.size)
	if flight.ranges != nil {
		retained := flight.ranges.bytes()
		flight.progress.mu.Lock()
		flight.progress.bytes = retained
		flight.progress.mu.Unlock()
		flight.progress.setHost(flight.ranges.video.host)
		flight.log.Info("local_blocks_reused", "retained_bytes", retained)
	}
	publishErr := s.publishScheduled(ctx, path, v, flight)
	if err := publishErr; err != nil {
		return "", "", err
	}
	flight.progress.setStage("index")
	indexCtx, stopIndex := context.WithTimeout(ctx, s.cfg.DownloadTimeout)
	defer stopIndex()
	if err := s.recordVideo(indexCtx, v); err != nil {
		return "", "", err
	}
	flight.log.Info("download_published", "key", v.key, "size", v.size)
	return path, "MISS", nil
}

// A sequential response uses the same complete-block store as Range downloads.
// Incomplete blocks remain in memory and are discarded when execution stops.
func (s *Server) publish(ctx context.Context, src io.Reader, path string, v video, flight *flight) error {
	if flight.ranges == nil {
		d, err := s.newLocalResource(v, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}, flight.log)
		if err != nil {
			return err
		}
		flight.ranges = d
	}
	sp := flight.ranges.makeSpool(flight.sequentialDemand)
	flight.spool = sp
	close(flight.streaming)
	flight.progress.setStage("download")
	reader := contextReader{ctx, progressReader{upstreamReader{src}, flight.progress}}
	buffer := s.rangeBuffer()
	defer func() {
		select {
		case s.rangeBuffers <- buffer:
		default:
		}
	}()
	for block := int64(0); block < blockCount(v.size); block++ {
		size := blockBytes(v.size, block)
		if _, err := io.ReadFull(reader, buffer[:size]); err != nil {
			return fmt.Errorf("%w: sequential block: %w", errUpstreamDownload, err)
		}
		// Require EOF before exposing the final block; an oversized response is invalid.
		if block == blockCount(v.size)-1 {
			if _, err := io.ReadFull(reader, buffer[size:size+1]); err != io.EOF {
				if err == nil {
					err = errors.New("oversized sequential response")
				}
				return fmt.Errorf("%w: %w", errUpstreamDownload, err)
			}
		}
		if _, err := sp.WriteAt(buffer[:size], block*rangeBlockSize); err != nil {
			return err
		}
	}
	ctx, stopPublication := context.WithTimeout(ctx, s.cfg.DownloadTimeout)
	defer stopPublication()
	return s.publishBlocks(ctx, path, v, flight)
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
