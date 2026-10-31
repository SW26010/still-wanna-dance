package cacheproxy

import (
	"testing"
	"time"

	"still-wanna-dance/internal/playbacksource"
)

func TestHTTPPlaybackReusesAndReplacesLegacyEvidence(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := t.Context()
	at := time.Now().Add(-time.Minute)
	old := SongURL{SongID: 42, API: legacyUdonAPI, Node: "cf", URL: "https://play.udon.dance/files/1/42-video.mp4?e=0123456789abcdef0123456789abcdef&s=12", QueryStartedAt: at, ObservedAt: at}
	if err := s.ObserveSongURL(ctx, old); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.SongMetadataCandidates(ctx, playbacksource.DefaultAPI); err != nil || len(pending) != 0 {
		t.Fatal("protocol policy must not refill complete metadata", pending, err)
	}
	urls, err := s.SongURLs(ctx, 42, "cf")
	if err != nil || len(urls) != 1 || urls[0].URL != old.URL {
		t.Fatal(urls, err)
	}
	fresh := old
	fresh.API = playbacksource.DefaultAPI
	fresh.QueryStartedAt, fresh.ObservedAt = at.Add(time.Second), at.Add(time.Second)
	if err := s.ObserveSongURL(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	// A late result or rejection using the old identity cannot undo the new row.
	old.ObservedAt = at.Add(2 * time.Second)
	if err := s.ObserveSongURL(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := s.RejectSongURL(ctx, old); err != nil {
		t.Fatal(err)
	}
	urls, err = s.SongURLs(ctx, 42, "cf")
	if err != nil || len(urls) != 1 || urls[0].API != fresh.API || urls[0].URL != fresh.URL {
		t.Fatal("fresh observation not authoritative", urls, err)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM song_urls WHERE song_id=42").Scan(&count); err != nil || count != 1 {
		t.Fatal("protocol change duplicated evidence", count, err)
	}
}
