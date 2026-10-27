package cacheproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

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
func (s *Server) requestVideo(r *http.Request, observed func(), current *currentGET) (video, func(), error) {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host != "api.udon.dance" {
		if !s.resourceHostAllowed(host) {
			return video{}, nil, errors.New("resource host has not been returned by the API")
		}
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
	current.identify(strconv.FormatInt(n, 10), "")
	// A valid playback request has reached us even if resolution stalls or
	// fails. Observe it before contacting upstream or looking for old cache.
	if observed != nil {
		observed()
	}
	v, release, err := s.loadSong(r.Context(), id)
	v.songID = strconv.FormatInt(n, 10) // Preserve validated identity even when resolution fails.
	return v, release, err
}

// Only a confirmed song association may select a local file. loadSong pins and
// verifies it before returning a local-only video; this lookup does not resolve
// an upstream URL or download missing content.
func (s *Server) localPlaybackVideo(ctx context.Context, id string) (video, error) {
	var key string
	if err := s.usage.db.QueryRowContext(ctx, "SELECT md5 FROM song_media WHERE song_id=?", id).Scan(&key); err != nil || !validMD5(key) {
		return video{}, errPlaybackUpstream
	}
	info, err := os.Stat(s.cfg.videoFile(key))
	if err != nil || !info.Mode().IsRegular() {
		return video{}, errPlaybackUpstream
	}
	return video{key: key, size: info.Size(), songID: id, localOnly: true}, nil
}
