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
func (s *Server) requestVideo(r *http.Request) (video, error) {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host != "api.udon.dance" {
		return s.parse(r)
	}
	if r.URL.Path != "/Api/Songs/play" || r.URL.RawPath != "" {
		return video{}, errors.New("unsupported API path")
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return video{}, errors.New("invalid playback query")
	}
	if len(q["id"]) != 1 || len(q["node"]) > 1 {
		return video{}, errors.New("invalid playback query")
	}
	id, node := q.Get("id"), q.Get("node")
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return video{}, errors.New("invalid song ID")
	}
	if node != "" && node != "cf" && node != "nya" {
		return video{}, errors.New("unsupported playback node")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var target string
	if s.cfg.ResolvePlayback != nil {
		target, err = s.cfg.ResolvePlayback(ctx, id, node)
	} else {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.udon.dance/Api/Songs/play?"+q.Encode(), nil)
		var resp *http.Response
		resp, err = s.client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != 301 && resp.StatusCode != 302 && resp.StatusCode != 307 && resp.StatusCode != 308 {
				err = fmt.Errorf("API status %d", resp.StatusCode)
			}
			target = resp.Header.Get("Location")
		}
	}
	if err != nil {
		return s.localPlaybackVideo(r.Context(), strconv.FormatInt(n, 10))
	}
	resolved, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return s.localPlaybackVideo(r.Context(), strconv.FormatInt(n, 10))
	}
	if (resolved.URL.Scheme != "http" && resolved.URL.Scheme != "https") || resolved.URL.User != nil || resolved.URL.Fragment != "" {
		return s.localPlaybackVideo(r.Context(), strconv.FormatInt(n, 10))
	}
	v, err := s.parse(resolved)
	if err != nil {
		return s.localPlaybackVideo(r.Context(), strconv.FormatInt(n, 10))
	}
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
