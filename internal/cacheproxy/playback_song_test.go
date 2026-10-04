package cacheproxy

import (
	"testing"
)

func assertPlaybackSong(t *testing.T, s *Server, id, body string) {
	t.Helper()
	v := parsedVideo(t, s, body)
	var key string
	if err := s.usage.db.QueryRow(`SELECT version_key FROM current_videos WHERE song_id=?`, id).Scan(&key); err != nil || key != v.key {
		t.Fatalf("song %s: key=%s error=%v", id, key, err)
	}
	var count int
	if err := s.usage.db.QueryRow(`SELECT COUNT(*) FROM song_videos WHERE song_id=? AND version_key=?`, id, v.key).Scan(&count); err != nil || count != 1 {
		t.Fatalf("missing song resource reference: %d %v", count, err)
	}
}
