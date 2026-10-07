package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// loadSong is the common immediate-playback/queue address policy. The query is
// server-owned and keeps updating observations after an immediate local hit.
func (s *Server) loadSong(ctx context.Context, q url.Values) (video, func(), error) {
	id, err := strconv.ParseInt(q.Get("id"), 10, 64)
	if err != nil || id <= 0 {
		return video{}, nil, errors.New("invalid song ID")
	}
	canonical := strconv.FormatInt(id, 10)
	if local, err := s.localPlaybackVideo(ctx, canonical); err == nil {
		s.pinVideo(local)
		if _, err = s.verifiedFile(ctx, local); err == nil {
			s.startPlaybackCheck(q, local)
			return local, func() { s.releaseVideo(local) }, nil
		}
		s.releaseVideo(local)
	}
	if err := s.StorageError(); err != nil {
		return video{}, nil, err
	}
	check := s.startPlaybackCheck(q, video{songID: canonical})
	node := s.cfg.PlaybackNode
	urls, err := s.SongURLs(ctx, id, node)
	if err != nil {
		return video{}, nil, err
	}
	candidates := &songCandidates{remaining: urls, tried: make(map[string]bool)}
	if v, ok := s.nextSongCandidate(ctx, canonical, check, candidates); ok {
		return v, nil, nil
	}
	v, err := s.waitSongQuery(ctx, check)
	return v, nil, err
}

func (s *Server) waitSongQuery(ctx context.Context, check *playbackCheck) (video, error) {
	select {
	case <-ctx.Done():
		return video{}, ctx.Err()
	case <-check.done:
	}
	if check.err != nil {
		return video{}, fmt.Errorf("%w: %w", errPlaybackUpstream, check.err)
	}
	return check.v, nil
}

// Candidates belong to one load, including all pre-body retries.
type songCandidates struct {
	remaining []SongURL
	tried     map[string]bool
}

func videoAddress(v video) string { return v.host + v.path + "?" + v.query }
func (s *Server) nextSongCandidate(ctx context.Context, id string, check *playbackCheck, candidates *songCandidates) (video, bool) {
	for len(candidates.remaining) > 0 {
		o := candidates.remaining[0]
		candidates.remaining = candidates.remaining[1:]
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL, nil)
		if err != nil {
			continue
		}
		v, err := s.parseResolved(r)
		if err != nil || candidates.tried[videoAddress(v)] {
			continue
		}
		candidates.tried[videoAddress(v)] = true
		v.songID, v.cached, v.refresh, v.candidates = id, &o, check, candidates
		return v, true
	}
	return video{}, false
}
func (s *Server) retrySongURL(ctx context.Context, v video) (video, error) {
	if err := s.RejectSongURL(ctx, *v.cached); err != nil {
		return video{}, err
	}
	if v.candidates != nil {
		if next, ok := s.nextSongCandidate(ctx, v.songID, v.refresh, v.candidates); ok {
			return next, nil
		}
	}
	fresh, err := s.waitSongQuery(ctx, v.refresh)
	if err != nil {
		return video{}, err
	}
	if videoAddress(fresh) == videoAddress(v) || (v.candidates != nil && v.candidates.tried[videoAddress(fresh)]) {
		return video{}, errors.New("refreshed URL matches failed address")
	}
	return fresh, nil
}

// PrefetchPlayback shares address policy and flights with HTTP playback while
// retaining queue ownership; waiting here never counts as a playback request.
func (s *Server) PrefetchPlayback(ctx context.Context, id int64) (string, error) {
	if !s.beginRequest() {
		return "", context.Canceled
	}
	defer s.wg.Done()
	v, release, err := s.loadSong(ctx, url.Values{"id": {strconv.FormatInt(id, 10)}, "node": {"cf"}})
	if release != nil {
		defer release()
	}
	if err != nil {
		return "", err
	}
	if v.localOnly {
		s.retentionMu.Lock()
		s.rememberSongResourceLocked(v.songID, v.key)
		s.retentionMu.Unlock()
		return "HIT", nil
	}
	for {
		source, err := s.prefetchVideo(ctx, v)
		if err == nil || v.cached == nil || !errors.Is(err, errUpstreamDownload) || ctx.Err() != nil {
			return source, err
		}
		fresh, fallbackErr := s.retrySongURL(ctx, v)
		if fallbackErr != nil {
			return source, errors.Join(err, fmt.Errorf("cached URL refresh: %w", fallbackErr))
		}
		v = fresh
	}
}
