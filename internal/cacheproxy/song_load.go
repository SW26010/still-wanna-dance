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
	check := s.startPlaybackCheck(q, video{songID: canonical})
	node := s.cfg.PlaybackNode
	urls, err := s.SongURLs(ctx, id, node)
	if err != nil {
		return video{}, nil, err
	}
	for _, o := range urls {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL, nil)
		if err != nil {
			continue
		}
		v, err := s.parseResolved(r)
		if err != nil {
			continue
		}
		v.songID, v.cached, v.refresh = canonical, &o, check
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
	v := check.v
	if err := s.recordVideo(ctx, v); err != nil {
		return video{}, err
	}
	if err := s.recordSongVideo(ctx, v.songID, v); err != nil {
		return video{}, err
	}
	return v, nil
}

func (s *Server) retrySongURL(ctx context.Context, v video) (video, error) {
	if err := s.RejectSongURL(ctx, *v.cached); err != nil {
		return video{}, err
	}
	fresh, err := s.waitSongQuery(ctx, v.refresh)
	if err != nil {
		return video{}, err
	}
	if fresh.host == v.host && fresh.path == v.path && fresh.query == v.query {
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
	prefetch := func(v video) (string, error) {
		target := (&url.URL{Scheme: s.cfg.OriginScheme, Host: v.host, Path: v.path, RawQuery: v.query}).String()
		return s.PrefetchSong(ctx, v.songID, target)
	}
	source, err := prefetch(v)
	if err != nil && v.cached != nil && errors.Is(err, errUpstreamDownload) && ctx.Err() == nil {
		if fresh, fallbackErr := s.retrySongURL(ctx, v); fallbackErr == nil {
			return prefetch(fresh)
		} else {
			err = errors.Join(err, fmt.Errorf("cached URL refresh: %w", fallbackErr))
		}
	}
	return source, err
}
