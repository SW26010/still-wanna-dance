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

var errAddressLookupBudget = fmt.Errorf("address lookup budget exhausted: %w", context.DeadlineExceeded)

type playbackCheck struct {
	done      chan struct{}
	v         video
	err       error
	cancel    context.CancelFunc
	exclusive bool // guarded by Server.mu; false once another caller shares it
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
		if err := s.ObserveSongURL(ctx, PlaybackObservation{SongID: n, URL: target, Node: address.Node}); err != nil {
			return video{}, err
		}
	}
	return v, nil
}

func (s *Server) resolvePlaybackLogged(ctx, caller context.Context, id string, local video) (video, error) {
	log := s.cfg.Logger.With("trace_id", applog.TraceID(applog.WithTrace(caller)), "song_id", id)
	started := time.Now()
	v, err := s.resolvePlaybackVideo(ctx, id)
	message := "预加载地址查询成功"
	if err != nil {
		message = "预加载地址查询失败"
	}
	log.Debug(message, "event_name", "prefetch_resolved", "elapsed_ms", time.Since(started).Milliseconds(), "success", err == nil,
		"resource_key", v.key, "host", v.host, "error", applog.SafeError(err))
	if err != nil {
		log.Warn("播放资源检查失败", "event_name", "playback_check_failed", "error", applog.SafeError(err))
	} else if local.key != "" && v.key != local.key {
		log.Warn("歌曲资源指纹不一致", "event_name", "song_md5_mismatch", "mapped_md5", local.key, "url_md5", v.key)
	}
	return v, err
}

// Share in-flight checks across probes and range requests. Their lifetime is
// owned by the server, so serving cached bytes or disconnecting cannot cancel
// check. Fresh evidence updates future mappings without changing this response.
func (s *Server) startPlaybackCheck(caller context.Context, id string, local video) *playbackCheck {
	check, _ := s.startPlaybackCheckOwned(caller, id, local, false)
	return check
}

func (s *Server) startPlaybackCheckOwned(caller context.Context, id string, local video, batch bool) (*playbackCheck, bool) {
	// Source policy is fixed for this process; only song identity keys a check.
	n, _ := strconv.ParseInt(id, 10, 64)
	id = strconv.FormatInt(n, 10)
	key := id
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := caller.Err(); err != nil {
		check := &playbackCheck{done: make(chan struct{}), err: err}
		close(check.done)
		return check, false
	}
	if check := s.playbackChecks[key]; check != nil {
		check.exclusive = false
		return check, false
	}
	check := &playbackCheck{done: make(chan struct{})}
	if s.closed {
		check.err = context.Canceled
		close(check.done)
		return check, false
	}
	if s.playbackChecks == nil {
		s.playbackChecks = make(map[string]*playbackCheck)
	}
	ctx, cancel := context.WithTimeout(s.ctx, playbackResolveBudget)
	check.cancel, check.exclusive = cancel, batch
	s.playbackChecks[key] = check
	// An abandoned waiter cannot cancel a shared lookup.
	if local.key != "" {
		s.pinVideo(local)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
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
		check.v, check.err = s.resolvePlaybackLogged(ctx, caller, id, local)
		close(check.done)
	}()
	return check, batch
}

// An exclusive batch check follows cancellation until the batch finishes.
// Other callers sharing the check keep its server-owned lifetime.
func (s *Server) startBatchPlaybackCheck(caller context.Context, id string, local video) (*playbackCheck, func()) {
	check, owned := s.startPlaybackCheckOwned(caller, id, local, true)
	if !owned {
		return check, func() {}
	}
	n, _ := strconv.ParseInt(id, 10, 64)
	key := strconv.FormatInt(n, 10)
	cancelExclusive := func() {
		s.mu.Lock()
		var cancel context.CancelFunc
		if s.playbackChecks[key] == check && check.exclusive {
			delete(s.playbackChecks, key)
			cancel = check.cancel
		}
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	stop := context.AfterFunc(caller, cancelExclusive)
	return check, func() {
		if caller.Err() != nil {
			cancelExclusive()
		}
		stop()
	}
}

// Resolve through the same cached-address and API policy when a formerly local
// resource needs downloading again. The caller checks its expected identity.
func (s *Server) loadSongAddress(ctx context.Context, canonical string) (video, error) {
	if err := s.StorageError(); err != nil {
		return video{}, err
	}
	check := s.startPlaybackCheck(ctx, canonical, video{songID: canonical})
	id, _ := strconv.ParseInt(canonical, 10, 64)
	urls, err := s.Store.mediaURLs(ctx, "song", id, playbacksource.Nodes(), -1, nil)
	if err != nil {
		return video{}, err
	}
	for _, saved := range urls {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, saved.URL, nil)
		if err != nil {
			continue
		}
		if v, err := s.parseResolved(r); err == nil {
			v.songID, v.refresh = canonical, check
			return v, nil
		}
	}
	return s.waitSongQuery(ctx, check)
}

func (s *Server) waitSongQuery(ctx context.Context, check *playbackCheck) (video, error) {
	select {
	case <-ctx.Done():
		select {
		case <-check.done:
			if errors.Is(check.err, ErrLocalStorage) {
				return video{}, check.err
			}
		default:
		}
		return video{}, ctx.Err()
	case <-check.done:
	}
	if check.err != nil {
		if errors.Is(check.err, ErrLocalStorage) {
			return video{}, check.err
		}
		return video{}, fmt.Errorf("%w: %w", errPlaybackUpstream, check.err)
	}
	return check.v, nil
}

// Resource address lookup is request-owned and pinned to content identity.
// Lookup aliases never become the download's song identity.
type resourceAddresses struct {
	key            string
	preferredSong  string
	preferredCheck *playbackCheck
	batch          bool
	selectedSongs  map[string]struct{}
	size           int64
	candidates     []MediaURL
	songs          []string
	tried          map[string]bool
}

func (p *resourceAddresses) next(ctx context.Context, s *Server) (video, error) {
	var first video
	err := p.search(ctx, s, func(v video) bool {
		first = v
		return false
	})
	return first, err
}

// One continuous search shares its ordinary API budget across all yielded URLs.
// Returning false from yield leaves the cursor at the next candidate.
func (p *resourceAddresses) search(ctx context.Context, s *Server, yield func(video) bool) error {
	if p.tried == nil {
		p.tried = make(map[string]bool)
		// Known addresses are selected by resource, independent of the API
		// lookup budget or the number/order of songs sharing this content.
		var err error
		p.candidates, err = s.Store.mediaURLs(ctx, "media", p.key, playbacksource.Nodes(), 32, nil)
		if err != nil {
			return err
		}
		// Preview and retry keep their existing alias bound. Batch fill tries
		// each candidate within the caller's lifetime and per-query timeout.
		limit := 8
		if p.batch {
			limit = -1
		}
		songs, err := s.Store.resourceSongs(ctx, p.key, p.preferredSong, limit)
		if err != nil {
			return err
		}
		// Keep the explicit song first even if its mapping has since changed.
		_, selected := p.selectedSongs[p.preferredSong]
		if p.preferredSong != "" && (p.preferredCheck != nil || p.selectedSongs == nil || selected) {
			p.songs = append(p.songs, p.preferredSong)
		}
		for _, id := range songs {
			if id == p.preferredSong {
				continue
			}
			if p.selectedSongs != nil {
				if _, ok := p.selectedSongs[id]; !ok {
					continue
				}
			}
			p.songs = append(p.songs, id)
		}
	}
	visit := func(v video) bool {
		key := v.requestURL()
		if v.key != p.key || p.size > 0 && v.size != p.size || p.tried[key] {
			return true
		}
		p.tried[key] = true
		p.size = v.size
		v.songID, v.refresh = "", nil
		return yield(v)
	}
	for len(p.candidates) > 0 {
		o := p.candidates[0]
		p.candidates = p.candidates[1:]
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, o.URL, nil)
		if err != nil {
			continue
		}
		v, err := s.parseResolved(r)
		if err == nil && !visit(v) {
			return nil
		}
	}
	if !p.batch {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, playbackResolveBudget, errAddressLookupBudget)
		defer cancel()
	}
	for len(p.songs) > 0 {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		id := p.songs[0]
		p.songs = p.songs[1:]
		lookupCtx := ctx
		cancelLookup := func() {}
		if p.batch {
			lookupCtx, cancelLookup = context.WithTimeout(ctx, playbackResolveBudget)
		}
		check := p.preferredCheck
		finishCheck := func() {}
		if check == nil || id != p.preferredSong {
			if p.batch {
				check, finishCheck = s.startBatchPlaybackCheck(lookupCtx, id, video{})
			} else {
				check = s.startPlaybackCheck(ctx, id, video{})
			}
			if id == p.preferredSong {
				p.preferredCheck = check
			}
		}
		v, err := s.waitSongQuery(lookupCtx, check)
		finishCheck()
		cancelLookup()
		if errors.Is(err, ErrLocalStorage) {
			return err
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		// The server-owned check may stop before server cancellation reaches
		// the request's asynchronously canceled context.
		if err := s.ctx.Err(); err != nil {
			return err
		}
		if err == nil && !visit(v) {
			return nil
		}
		// A valid URL for a newer version remains valid evidence. It is simply
		// not eligible for this pinned resource; never reject or switch to it.
	}
	return fmt.Errorf("%w: 当前资源没有可用地址或可解析的歌曲关联，请更新曲库后重试", errPlaybackUpstream)
}
