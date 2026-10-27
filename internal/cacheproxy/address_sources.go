package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/playbacksource"
)

var errPlaybackUpstream = errors.New("playback upstream failed")

const playbackResolveBudget = 30 * time.Second

type playbackCheck struct {
	done chan struct{}
	v    video
	err  error
}

// Resolve fresh evidence through the application adapter or the standalone
// resolver. Both obey playbacksource; neither knows about block scheduling.
func (s *Server) resolvePlaybackVideo(ctx context.Context, id string) (video, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return video{}, errors.New("invalid song ID")
	}
	var target string
	var address playbacksource.Address
	if s.cfg.ResolvePlayback != nil {
		target, err = s.cfg.ResolvePlayback(ctx, strconv.FormatInt(n, 10))
	} else {
		resolver := playbacksource.Resolver{
			MaxFileBytes: s.cfg.MaxFileBytes, AttemptTimeout: playbackResolveBudget,
			Clients: func(context.Context, string) []*http.Client { return []*http.Client{s.client} },
		}
		address, err = resolver.Resolve(ctx, n)
		target = address.URL
	}
	if err != nil {
		return video{}, err
	}
	resolved, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return video{}, err
	}
	if (resolved.URL.Scheme != "http" && resolved.URL.Scheme != "https") || resolved.URL.User != nil || resolved.URL.Fragment != "" {
		return video{}, errPlaybackUpstream
	}
	v, err := s.parseResolved(resolved)
	if err != nil {
		return video{}, err
	}
	v.songID = strconv.FormatInt(n, 10)
	if s.cfg.ResolvePlayback == nil {
		if err := s.ObserveSongURL(ctx, SongURL{SongID: n, URL: target, API: address.API, Node: address.Node, QueryStartedAt: address.QueryStartedAt, ObservedAt: address.ObservedAt}); err != nil {
			return video{}, err
		}
	}
	return v, nil
}

// Share in-flight checks across probes and range requests. Their lifetime is
// owned by the server, so serving cached bytes or disconnecting cannot cancel
// diagnostic check. Differences are logged without changing content or mappings.
func (s *Server) startPlaybackCheck(id string, local video) *playbackCheck {
	// Source policy is fixed for this process; only song identity keys a check.
	n, _ := strconv.ParseInt(id, 10, 64)
	id = strconv.FormatInt(n, 10)
	key := id
	s.mu.Lock()
	defer s.mu.Unlock()
	if check := s.playbackChecks[key]; check != nil {
		return check
	}
	check := &playbackCheck{done: make(chan struct{})}
	if s.closed {
		check.err = context.Canceled
		close(check.done)
		return check
	}
	if s.playbackChecks == nil {
		s.playbackChecks = make(map[string]*playbackCheck)
	}
	s.playbackChecks[key] = check
	if local.key != "" {
		s.pinVideo(local)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			if s.playbackChecks[key] == check {
				delete(s.playbackChecks, key)
			}
			s.mu.Unlock()
		}()
		if local.key != "" {
			defer s.releaseVideo(local)
		}
		ctx, cancel := context.WithTimeout(s.ctx, playbackResolveBudget)
		check.v, check.err = s.resolvePlaybackVideo(ctx, id)
		cancel()
		if check.err != nil {
			s.cfg.Logger.Warn("playback_check_failed", "song_id", local.songID, "error", applog.SafeError(check.err))
		} else if local.key != "" && check.v.key != local.key {
			s.cfg.Logger.Warn("song_md5_mismatch", "song_id", local.songID, "mapped_md5", local.key, "url_md5", check.v.key)
		}
		close(check.done)
	}()
	return check
}

// Resolve through the same cached-address and API policy when a formerly local
// resource needs downloading again. The caller checks its expected identity.
func (s *Server) loadSongAddress(ctx context.Context, canonical string) (video, error) {
	if err := s.StorageError(); err != nil {
		return video{}, err
	}
	check := s.startPlaybackCheck(canonical, video{songID: canonical})
	id, _ := strconv.ParseInt(canonical, 10, 64)
	urls, err := s.songURLs(ctx, "song_id", id, playbacksource.Nodes(), -1)
	if err != nil {
		return video{}, err
	}
	candidates := &songCandidates{remaining: urls, tried: make(map[string]bool)}
	if v, ok := s.nextSongCandidate(ctx, canonical, check, candidates); ok {
		return v, nil
	}
	return s.waitSongQuery(ctx, check)
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

// Resource address lookup is request-owned and pinned to content identity.
// Lookup aliases never become the download's song identity.
type resourceAddresses struct {
	key        string
	size       int64
	candidates []SongURL
	songs      []string
	tried      map[string]bool
}

func (p *resourceAddresses) next(ctx context.Context, s *Server, failed video) (video, error) {
	if failed.cached != nil {
		if err := s.RejectSongURL(ctx, *failed.cached); err != nil {
			return video{}, err
		}
	}
	if p.tried == nil {
		p.tried = make(map[string]bool)
		// Known addresses are selected by resource, independent of the API
		// lookup budget or the number/order of songs sharing this content.
		var err error
		p.candidates, err = s.songURLs(ctx, "md5", p.key, playbacksource.Nodes(), 32)
		if err != nil {
			return video{}, err
		}
		// Bound address work independently of the number of aliases in a library.
		rows, err := s.db.QueryContext(ctx, `SELECT song_id FROM song_media WHERE md5=?
 UNION SELECT song_id FROM song_urls WHERE md5=? ORDER BY song_id LIMIT 8`, p.key, p.key)
		if err != nil {
			return video{}, s.storageError(err)
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			p.songs = append(p.songs, id)
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return video{}, s.storageError(err)
		}
	}
	if failed.host != "" {
		p.tried[videoAddress(failed)] = true
	}
	accept := func(v video) (video, bool) {
		if v.key != p.key || p.size > 0 && v.size != p.size || p.tried[videoAddress(v)] {
			return video{}, false
		}
		p.tried[videoAddress(v)] = true
		p.size = v.size
		v.songID, v.refresh, v.candidates = "", nil, nil
		return v, true
	}
	for len(p.candidates) > 0 {
		o := p.candidates[0]
		p.candidates = p.candidates[1:]
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL, nil)
		if err != nil {
			continue
		}
		v, err := s.parseResolved(r)
		if err == nil {
			v.cached = &o
			if v, ok := accept(v); ok {
				return v, nil
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, playbackResolveBudget)
	defer cancel()
	for len(p.songs) > 0 {
		id := p.songs[0]
		p.songs = p.songs[1:]
		check := s.startPlaybackCheck(id, video{})
		v, err := s.waitSongQuery(ctx, check)
		if ctx.Err() != nil {
			return video{}, ctx.Err()
		}
		if err == nil {
			if v, ok := accept(v); ok {
				return v, nil
			}
		}
		// A valid URL for a newer version remains valid evidence. It is simply
		// not eligible for this pinned resource; never reject or switch to it.
	}
	return video{}, fmt.Errorf("%w: 当前资源版本没有可用的下载地址，请更新曲库后重新预览", errPlaybackUpstream)
}
