package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var errPlaybackUpstream = errors.New("playback upstream failed")

const playbackWaitBudget = 5 * time.Second
const playbackResolveBudget = 30 * time.Second

type playbackCheck struct {
	done     chan struct{}
	accepted chan bool
	v        video
	err      error
}

type playbackSong struct {
	done chan struct{}
	err  error
}

// Called under mu before exposing the flight to playback. Each explicit song
// gets its own record even when several songs share one resource download.
// Keep the resource pinned until the record commits, independently of the
// HTTP request's lifetime (resolvers commonly disconnect after a short read).
func (s *Server) attachPlaybackSongLocked(f *flight, v video) {
	if f.playbackSongs == nil {
		f.playbackSongs = make(map[string]*playbackSong)
	}
	if f.playbackSongs[v.songID] != nil {
		return
	}
	record := &playbackSong{done: make(chan struct{})}
	f.playbackSongs[v.songID] = record
	s.pinVideo(v)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.releaseVideo(v)
		defer close(record.done)
		<-f.done
		if f.err != nil {
			record.err = f.err
			return
		}
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer cancel()
		record.err = s.recordSongVideo(ctx, v.songID, v)
		if record.err != nil {
			s.cfg.Logger.Error("playback_song_failed", "song_id", v.songID, "resource_key", v.key, "error", record.err)
		}
	}()
}

func (s *Server) waitPlaybackSong(ctx context.Context, f *flight, id string) error {
	s.mu.Lock()
	record := f.playbackSongs[id]
	s.mu.Unlock()
	if record == nil {
		return nil
	}
	select {
	case <-record.done:
		return record.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// The API song ID is authoritative; the resource ID in a video path is not.
// A non-nil release transfers the verified local pin to the caller, which must
// hold it until the response finishes, including early returns.
func (s *Server) requestVideo(r *http.Request, observed func()) (video, func(), error) {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host != "api.udon.dance" {
		v, err := s.parse(r)
		if err == nil && observed != nil {
			observed()
		}
		return v, nil, err
	}
	if r.URL.Path != "/Api/Songs/play" || r.URL.RawPath != "" {
		return video{}, nil, errors.New("unsupported API path")
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return video{}, nil, errors.New("invalid playback query")
	}
	if len(q["id"]) != 1 || len(q["node"]) > 1 {
		return video{}, nil, errors.New("invalid playback query")
	}
	id, node := q.Get("id"), q.Get("node")
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return video{}, nil, errors.New("invalid song ID")
	}
	if node != "" && node != "cf" && node != "nya" {
		return video{}, nil, errors.New("unsupported playback node")
	}
	// A valid playback request has reached us even if resolution stalls or
	// fails. Observe it before contacting upstream or looking for old cache.
	if observed != nil {
		observed()
	}
	// Only verified local bytes allow the foreground wait to end early.
	if local, localErr := s.localPlaybackVideo(r.Context(), strconv.FormatInt(n, 10)); localErr == nil {
		s.pinVideo(local)
		handoff := false
		release := func() { s.releaseVideo(local) }
		defer func() {
			if !handoff {
				release()
			}
		}()
		if _, localErr = s.verifiedFile(r.Context(), local); localErr == nil {
			check := s.startPlaybackCheck(q, local)
			accepted := false
			defer func() {
				select {
				case check.accepted <- accepted:
				default:
				}
			}()
			timer := time.NewTimer(playbackWaitBudget)
			defer timer.Stop()
			select {
			case <-check.done:
				if err := r.Context().Err(); err != nil {
					return video{}, nil, err
				}
				if check.err == nil {
					accepted = true
					if check.v.key == local.key {
						handoff = true
						return check.v, release, nil
					}
					return check.v, nil, nil
				}
				handoff = true
				return local, release, nil
			case <-timer.C:
				handoff = true
				return local, release, nil
			case <-r.Context().Done():
				return video{}, nil, r.Context().Err()
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), playbackResolveBudget)
	defer cancel()
	v, err := s.resolvePlaybackVideo(ctx, q)
	if err != nil {
		v, err := s.localPlaybackVideo(r.Context(), strconv.FormatInt(n, 10))
		return v, nil, err
	}
	return v, nil, nil
}

// Share in-flight checks across probes and range requests. Their lifetime is
// owned by the server, so serving cached bytes or disconnecting cannot cancel
// the update check. A changed version is refreshed if a foreground waiter
// returns without accepting it, regardless of how quickly resolution finishes.
func (s *Server) startPlaybackCheck(q url.Values, local video) *playbackCheck {
	key := q.Encode()
	s.mu.Lock()
	defer s.mu.Unlock()
	if check := s.playbackChecks[key]; check != nil {
		return check
	}
	check := &playbackCheck{done: make(chan struct{}), accepted: make(chan bool, 1)}
	if s.closed {
		check.err = context.Canceled
		close(check.done)
		return check
	}
	if s.playbackChecks == nil {
		s.playbackChecks = make(map[string]*playbackCheck)
	}
	s.playbackChecks[key] = check
	s.pinVideo(local)
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
		defer s.releaseVideo(local)
		ctx, cancel := context.WithTimeout(s.ctx, playbackResolveBudget)
		check.v, check.err = s.resolvePlaybackVideo(ctx, q)
		cancel()
		refresh := check.err == nil && check.v.key != local.key
		close(check.done)
		if refresh {
			// Wait for an actual foreground decision rather than guessing from
			// elapsed time. Canceled and timed-out callers also report here.
			select {
			case accepted := <-check.accepted:
				if accepted {
					return
				}
			case <-s.ctx.Done():
				return
			}
			ctx, cancel := context.WithTimeout(s.ctx, s.cfg.DownloadTimeout)
			defer cancel()
			target := "https://" + check.v.host + check.v.path + "?" + check.v.query
			// Re-confirm after download to prevent a late result rolling back a
			// newer version. This background confirmation may also be slow.
			if _, err := s.prefetchWithConfirmationBudget(ctx, local.songID, target, playbackResolveBudget); err != nil {
				s.cfg.Logger.Warn("playback_refresh_failed", "song_id", local.songID, "error", err)
			}
		}
	}()
	return check
}

func (s *Server) resolvePlaybackVideo(ctx context.Context, q url.Values) (video, error) {
	id, node := q.Get("id"), q.Get("node")
	var err error
	var target string
	if s.cfg.ResolvePlayback != nil {
		target, err = s.cfg.ResolvePlayback(ctx, id, node)
	} else {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.udon.dance/Api/Songs/play?"+q.Encode(), nil)
		var resp *http.Response
		resp, err = s.client.Do(req)
		if err == nil {
			resp.Body.Close()
			if !IsSongRedirect(resp.StatusCode) {
				err = fmt.Errorf("API status %d", resp.StatusCode)
			}
			target = resp.Header.Get("Location")
		}
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
	v, err := s.parse(resolved)
	if err != nil {
		return video{}, err
	}
	n, _ := strconv.ParseInt(id, 10, 64)
	v.songID = strconv.FormatInt(n, 10)
	return v, nil
}

// Only a previously confirmed song association may supply an offline version.
// Re-parse stored metadata to apply the same limits as online playback. The
// handler pins and verifies the file, and must never download or promote it.
func (s *Server) localPlaybackVideo(ctx context.Context, id string) (video, error) {
	var key, checksum, path string
	var size int64
	err := s.usage.db.QueryRowContext(ctx, `SELECT v.version_key, v.checksum, v.file_bytes, v.source_path
 FROM current_videos c JOIN video_versions v ON v.version_key=c.version_key
 WHERE c.song_id=?`, id).Scan(&key, &checksum, &size, &path)
	if err != nil {
		return video{}, errPlaybackUpstream
	}
	u := &url.URL{Scheme: "http", Host: "play.udon.dance", Path: path,
		RawQuery: url.Values{"e": {checksum}, "s": {strconv.FormatInt(size, 10)}}.Encode()}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return video{}, errPlaybackUpstream
	}
	v, err := s.parse(r)
	if err != nil || v.key != key {
		return video{}, errPlaybackUpstream
	}
	v.songID, v.localOnly = id, true
	return v, nil
}
