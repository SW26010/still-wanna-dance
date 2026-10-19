package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/blocksched"
)

// Prefetch uses exactly the same validation, shared downloads and publication as playback.
// Canceling the waiter leaves an already shared download running for other clients.
func (s *Server) Prefetch(ctx context.Context, target string) (source string, resultErr error) {
	return s.prefetch(ctx, "", target)
}

// PrefetchSong associates a validated resource with the explicit catalog song ID.
func (s *Server) PrefetchSong(ctx context.Context, id, target string) (string, error) {
	if n, err := strconv.ParseInt(id, 10, 64); err != nil || n <= 0 {
		return "", errors.New("invalid song ID")
	}
	return s.prefetch(ctx, id, target)
}

func (s *Server) prefetch(ctx context.Context, id, target string) (source string, resultErr error) {
	if !s.beginRequest() {
		return "", context.Canceled
	}
	defer s.wg.Done()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", applog.SafeError(err)
	}
	v, err := s.parseResolved(r)
	if err != nil {
		return "", err
	}
	v.songID = id
	return s.prefetchVideo(ctx, v)
}

// prefetchVideo consumes metadata validated by the caller's registered request.
func (s *Server) prefetchVideo(ctx context.Context, v video) (source string, resultErr error) {
	start := time.Now()
	ctx = applog.WithTrace(ctx)
	id := v.songID
	if expected, _ := ctx.Value(expectedMD5Key{}).(string); expected != "" && expected != v.key {
		s.cfg.Logger.Warn("song_md5_mismatch", "song_id", id, "mapped_md5", expected, "url_md5", v.key)
		return "", fmt.Errorf("上游 MD5 与清单不一致：期望 %s，收到 %s", expected, v.key)
	}
	finishBudget, err := s.reserveBatchVideo(ctx, v)
	if err != nil {
		return "", err
	}
	// Demand cleanup cancels its derived context even after an ordinary failure.
	// Only caller cancellation should keep an unsuccessful reservation charged.
	defer func(callerCtx context.Context) {
		finishBudget(resultErr == nil || callerCtx.Err() != nil)
	}(ctx)
	log := s.cfg.Logger.With("trace_id", applog.TraceID(ctx), "song_id", id, "resource_key", v.key)
	log.Info("prefetch_started")
	demand, releaseDemand, err := s.addPreparation(ctx, v)
	if err != nil {
		return "", err
	}
	defer releaseDemand()
	ctx = context.WithValue(demand.ctx, sourceDemandKey{}, demand)
	preparationAdmitted(ctx)
	s.pinVideo(v)
	defer s.releaseVideo(v)
	defer func() {
		outcome := "completed"
		if resultErr != nil {
			outcome = "failed"
		}
		if errors.Is(resultErr, ErrNotRetained) {
			outcome = "not_retained"
		}
		if ctx.Err() != nil && !errors.Is(context.Cause(ctx), blocksched.ErrExpired) {
			outcome = "canceled"
		}
		cache := source
		if cache == "" {
			cache = "UNKNOWN"
		}
		log.Info("prefetch_finished", "outcome", outcome, "cache", cache, "elapsed_ms", time.Since(start).Milliseconds(), "error", applog.SafeError(resultErr))
		s.usage.record(usageEvent{id: v.key, at: start.UnixMilli(), key: v.key, host: v.host,
			source: "prefetch", method: "GET", size: v.size, cache: cache,
			outcome: outcome, elapsedMS: time.Since(start).Milliseconds()})
	}()
retryDownload:
	f, _, err := s.obtainMode(ctx, v, true)
	if errors.Is(err, errPreempted) || errors.Is(err, blocksched.ErrExpired) && ctx.Err() == nil {
		goto retryDownload
	}
	if errors.Is(err, ErrNotRetained) && demand.queue {
		return "TEMP", nil
	}
	if err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", context.Cause(ctx)
	case <-f.done:
		if errors.Is(f.err, errPreempted) || errors.Is(f.err, blocksched.ErrExpired) && ctx.Err() == nil {
			goto retryDownload
		}
		if errors.Is(f.err, ErrNotRetained) && demand.queue {
			return "TEMP", nil
		}
		if f.err != nil {
			return f.source, f.err
		}
		if id != "" {
			if err := s.recordSongVideo(ctx, id, v); err != nil {
				return f.source, err
			}
		}
		return f.source, nil
	}
}
