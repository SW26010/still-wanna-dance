package cacheproxy

import (
	"context"
	"testing"
	"time"
)

func TestSongURLPersistenceOrderingAndFailure(t *testing.T) {
	s, cfg := setup(t, nil)
	ctx := context.Background()
	started := time.Now().Add(-time.Minute)
	o := SongURL{SongID: 42, API: "https://api.udon.dance/Api/Songs/play", Node: "cf", URL: "https://play.udon.dance/files/1/42-video.mp4?e=0123456789abcdef0123456789abcdef&s=12&token=keep-me", QueryStartedAt: started, ObservedAt: started.Add(time.Second)}
	if err := s.ObserveSongURL(ctx, o); err != nil {
		t.Fatal(err)
	}
	newer := o
	newer.QueryStartedAt, newer.ObservedAt = started.Add(2*time.Second), started.Add(3*time.Second)
	if err := s.ObserveSongURL(ctx, newer); err != nil {
		t.Fatal(err)
	}
	// Older query finishes last and even reports a later completion time.
	o.ObservedAt = started.Add(4 * time.Second)
	if err := s.ObserveSongURL(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := s.RejectSongURL(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	urls, err := s.SongURLs(ctx, 42, "cf")
	if err != nil || len(urls) != 1 {
		t.Fatalf("%+v %v", urls, err)
	}
	if urls[0].URL != newer.URL || !urls[0].ObservedAt.Equal(newer.ObservedAt) || urls[0].Size != 12 || urls[0].MD5 != "0123456789abcdef0123456789abcdef" {
		t.Fatal(urls)
	}
	if err := s.RejectSongURL(ctx, urls[0]); err != nil {
		t.Fatal(err)
	}
	if urls, err := s.SongURLs(ctx, 42, ""); err != nil || len(urls) != 0 {
		t.Fatal(urls, err)
	}
	newer.QueryStartedAt, newer.ObservedAt = time.Now(), time.Now()
	if err := s.ObserveSongURL(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if urls, err := s.SongURLs(ctx, 42, ""); err != nil || len(urls) != 1 {
		t.Fatal(urls, err)
	}
	if urls, err := s.SongURLs(ctx, 42, "nya"); err != nil || len(urls) != 0 {
		t.Fatal(urls, err)
	}
}

func TestSongURLConflictPreservesMapping(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	const expected = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := s.SyncCatalog(ctx, testCandidate("20261007000000", CatalogSong{ID: 42, MD5: expected})); err != nil {
		t.Fatal(err)
	}
	o := SongURL{SongID: 42, API: "https://api.udon.dance/Api/Songs/play", Node: "nya", URL: "https://nya.xin.moe/files/1/42-video.mp4?e=0123456789abcdef0123456789abcdef&s=12", QueryStartedAt: time.Now(), ObservedAt: time.Now()}
	if err := s.ObserveSongURL(ctx, o); err != nil {
		t.Fatal(err)
	}
	var mapped string
	if err := s.usage.db.QueryRow("SELECT md5 FROM song_media WHERE song_id=42").Scan(&mapped); err != nil || mapped != expected {
		t.Fatal(mapped, err)
	}
	urls, err := s.SongURLs(ctx, 42, "")
	if err != nil || len(urls) != 1 || urls[0].MD5 == expected {
		t.Fatal(urls, err)
	}
	o.URL = "https://evil.invalid/file"
	if err := s.ObserveSongURL(ctx, o); err == nil {
		t.Fatal("invalid URL accepted")
	}
}
