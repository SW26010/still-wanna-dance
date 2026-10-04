package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"still-wanna-dance/internal/applog"
)

var errPlaybackUpstream = errors.New("playback upstream failed")

const playbackResolveBudget = 30 * time.Second

type playbackCheck struct {
	done chan struct{}
	v    video
	err  error
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
	// Local content is returned immediately. The upstream check is diagnostic only.
	if local, localErr := s.localPlaybackVideo(r.Context(), strconv.FormatInt(n, 10)); localErr == nil {
		s.pinVideo(local)
		if _, localErr = s.verifiedFile(r.Context(), local); localErr == nil {
			s.startPlaybackCheck(q, local)
			return local, func() { s.releaseVideo(local) }, nil
		}
		s.releaseVideo(local)
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
// diagnostic check. Differences are logged without changing content or mappings.
func (s *Server) startPlaybackCheck(q url.Values, local video) *playbackCheck {
	key := q.Encode()
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
		check.v, check.err = s.resolvePlaybackVideoReadOnly(ctx, q)
		cancel()
		if check.err != nil {
			s.cfg.Logger.Warn("playback_check_failed", "song_id", local.songID, "error", applog.SafeError(check.err))
		} else if check.v.key != local.key {
			s.cfg.Logger.Warn("song_md5_mismatch", "song_id", local.songID, "mapped_md5", local.key, "url_md5", check.v.key)
		}
		close(check.done)
	}()
	return check
}

func (s *Server) resolvePlaybackVideo(ctx context.Context, q url.Values) (video, error) {
	v, err := s.resolvePlaybackVideoReadOnly(ctx, q)
	if err != nil {
		return v, err
	}
	if err := s.recordVideo(ctx, v); err != nil {
		return video{}, err
	}
	if err := s.recordSongVideo(ctx, v.songID, v); err != nil {
		return video{}, err
	}
	return v, nil
}

func (s *Server) resolvePlaybackVideoReadOnly(ctx context.Context, q url.Values) (video, error) {
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
	var key string
	if err := s.usage.db.QueryRowContext(ctx, "SELECT version_key FROM current_videos WHERE song_id=?", id).Scan(&key); err != nil || !validMD5(key) {
		return video{}, errPlaybackUpstream
	}
	info, err := os.Stat(s.cfg.videoFile(key))
	if err != nil || !info.Mode().IsRegular() {
		return video{}, errPlaybackUpstream
	}
	return video{key: key, checksum: key, size: info.Size(), songID: id, localOnly: true, host: "play.udon.dance"}, nil
}
