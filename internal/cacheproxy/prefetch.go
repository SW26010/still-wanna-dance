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
// A queue TEMP result ends preparation, while the engine coordinates surviving usage.
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

// PrefetchDownload preserves the fill's captured MD5 while checking the explicit
// song for updates. Unresolved songs use the ordinary playback resolver.
func (s *Server) PrefetchDownload(ctx context.Context, id int64) (string, error) {
	ctx = applog.WithTrace(ctx)
	if key, _ := ctx.Value(expectedMD5Key{}).(string); key != "" {
		return s.prefetchResource(ctx, key, strconv.FormatInt(id, 10))
	}
	ctx, stopRequest := context.WithCancel(ctx)
	stopServer := context.AfterFunc(s.ctx, stopRequest)
	defer stopServer()
	defer stopRequest()
	if s.ctx.Err() != nil {
		return "", context.Canceled
	}
	if !s.beginRequest() {
		return "", context.Canceled
	}
	defer s.wg.Done()
	if err := s.CheckSongAllowed(ctx, id); err != nil {
		return "", err
	}
	if err := s.StorageError(); err != nil {
		return "", err
	}
	resolveCtx, cancel := context.WithTimeout(ctx, playbackResolveBudget)
	check, finishCheck := s.startBatchPlaybackCheck(resolveCtx, strconv.FormatInt(id, 10), video{})
	v, err := s.waitSongQuery(resolveCtx, check)
	finishCheck()
	cancel()
	if errors.Is(err, ErrLocalStorage) {
		return "", err
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		if errors.Is(err, errPlaybackUpstream) {
			return "", err
		}
		return "", fmt.Errorf("%w: %w", errPlaybackUpstream, err)
	}
	return s.prefetchVideo(withPreparationScope(ctx), v)
}

func (s *Server) prefetchResource(ctx context.Context, key, id string) (string, error) {
	if !s.beginRequest() {
		return "", context.Canceled
	}
	defer s.wg.Done()
	ctx, stopRequest := context.WithCancel(ctx)
	stopServer := context.AfterFunc(s.ctx, stopRequest)
	defer stopServer()
	defer stopRequest()
	if s.ctx.Err() != nil {
		return "", context.Canceled
	}
	target, err := s.PreviewMetadata(ctx, s.cfg.libraryPaths(), PreviewTarget{Resource: key, AdmissionSongID: id})
	if err != nil {
		return "", err
	}
	selected, _ := ctx.Value(fillCandidatesKey{}).(map[string]struct{})
	p := previewRequest{target: target, addresses: &resourceAddresses{key: key, size: target.Size, preferredSong: id, batch: true, selectedSongs: selected}}
	v, err := p.video(ctx, s)
	if err == nil && !v.localOnly && v.host == "" {
		v, err = p.nextAddress(ctx, s, video{})
	}
	if err == nil && id != "" && ctx.Err() == nil && p.addresses.preferredCheck == nil {
		// Reusing an address must still refresh the explicit song's mapping.
		// The download keeps v; retries reuse this result even after the shared
		// in-flight query has finished and left the server's lookup table.
		var finishCheck func()
		p.addresses.preferredCheck, finishCheck = s.startBatchPlaybackCheck(ctx, id, v)
		defer finishCheck()
	}
	for err == nil {
		// The API lookup alias is never the caller's song identity.
		v.songID = id
		source, downloadErr := s.prefetchVideo(ctx, v)
		if downloadErr == nil || !errors.Is(downloadErr, errUpstreamDownload) || ctx.Err() != nil {
			return source, downloadErr
		}
		v, err = p.nextAddress(ctx, s, v)
		if err != nil {
			return source, errors.Join(downloadErr, err)
		}
	}
	return "", err
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
	if id != "" {
		n, _ := strconv.ParseInt(id, 10, 64)
		if err := s.CheckSongAllowed(ctx, n); err != nil {
			return "", err
		}
	}
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
	log.Debug("prefetch_started")
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
