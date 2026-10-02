package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"still-wanna-dance/internal/applog"
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
	return s.prefetchWithConfirmationBudget(ctx, id, target, 5*time.Second)
}

func (s *Server) prefetchWithConfirmationBudget(ctx context.Context, id, target string, confirmationBudget time.Duration) (source string, resultErr error) {
	if !s.beginRequest() {
		return "", context.Canceled
	}
	defer s.wg.Done()
	start := time.Now()
	ctx = applog.WithTrace(ctx)
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", applog.SafeError(err)
	}
	v, err := s.parse(r)
	if err != nil {
		return "", err
	}
	v.songID = id
	// Background callers choose their preferred route and own full-file retries.
	// Preserve that choice even when playback has measured a faster mirror.
	v.preferRequestedRoute = true
	if id != "" {
		s.routeMu.Lock()
		s.routeSongs.put(v.key, id, time.Time{}, routeSongsLimit)
		s.routeMu.Unlock()
	}
	log := s.cfg.Logger.With("trace_id", applog.TraceID(ctx), "song_id", id, "resource_key", v.key)
	log.Info("prefetch_started")
	s.pinVideo(v)
	defer s.releaseVideo(v)
	if id != "" {
		// Queue ownership must outlive this cancellable waiter. Register it
		// while pinned, before a shared flight can finish and release its pin.
		// This is only a memory reservation; validation/current-version
		// confirmation still belongs to recordSongVideo after completion.
		s.retentionMu.Lock()
		s.rememberSongResourceLocked(id, v.key)
		s.retentionMu.Unlock()
	}
	defer func() {
		outcome := "completed"
		if resultErr != nil {
			outcome = "failed"
		}
		if ctx.Err() != nil {
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
	f, reader, err := s.obtainMode(ctx, v, true)
	if reader != nil {
		defer reader.Close()
	}
	if err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-f.done:
		if f.err != nil {
			return f.source, f.err
		}
		if id != "" {
			if err := s.recordSongVideoWithBudget(ctx, id, v, confirmationBudget); err != nil {
				return f.source, err
			}
		}
		return f.source, nil
	}
}
