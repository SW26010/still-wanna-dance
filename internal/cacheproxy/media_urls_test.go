package cacheproxy

import (
	"reflect"
	"strings"
	"testing"
)

func TestMediaURLsSharedIdentityCommitOrderAndRestart(t *testing.T) {
	s, cfg := setup(t, nil)
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	address := func(key, token string) string {
		return "https://play.udon.dance/files/1/42-video.mp4?e=" + key + "&s=12&token=" + token
	}
	write := func(id int64, key, node, token string) {
		t.Helper()
		if err := s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: id, Node: node, URL: address(key, token)}); err != nil {
			t.Fatal(err)
		}
	}
	write(42, a, "cf", "first")
	write(43, a, "cf", "shared")
	write(42, a, "nya", "other-node")
	for _, id := range []int64{42, 43} {
		urls, err := s.SongURLs(t.Context(), id, "cf")
		if err != nil || len(urls) != 1 || urls[0].URL != address(a, "shared") {
			t.Fatal(urls, err)
		}
	}
	write(42, b, "cf", "changed")
	assertSongResource(t, s, "43", a)
	// A later successful response overwrites this song even if its network query
	// began earlier. No timestamps, source-song hints or failed markers exist.
	write(42, a, "cf", "last-commit")
	assertSongResource(t, s, "42", a)
	old, err := s.ResourceURLs(t.Context(), b, "cf", 1)
	if err != nil || len(old) != 1 || old[0].URL != address(b, "changed") {
		t.Fatal(old, err)
	}
	all, err := s.AvailableMediaURLs(t.Context())
	if err != nil || len(all) != 2 {
		t.Fatal(all, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopenFixtureStore(t, &cfg)
	got, err := cfg.Store.AvailableMediaURLs(t.Context())
	if err != nil || !reflect.DeepEqual(got, all) {
		t.Fatal(got, all, err)
	}
	// Reading an orphan address doesn't reconstruct a song association.
	if _, err := cfg.Store.db.Exec("DELETE FROM song_media WHERE md5=?", a); err != nil {
		t.Fatal(err)
	}
	got, err = cfg.Store.ResourceURLs(t.Context(), a, "cf", 1)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
}

func TestMediaURLValidationDoesNotChangeExistingEvidence(t *testing.T) {
	s, _ := setup(t, nil)
	o := PlaybackObservation{SongID: 42, Node: "cf", URL: videoURL(payload)}
	if err := s.ObserveSongURL(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	before, err := s.SongURLs(t.Context(), 42, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []PlaybackObservation{{SongID: 42, URL: o.URL}, {SongID: 42, Node: "cf", URL: "invalid"}, {Node: "cf", URL: o.URL}} {
		if err := s.ObserveSongURL(t.Context(), bad); err == nil {
			t.Fatal("invalid observation accepted")
		}
	}
	after, err := s.SongURLs(t.Context(), 42, "")
	if err != nil || !reflect.DeepEqual(before, after) || s.StorageError() != nil {
		t.Fatal(after, err, s.StorageError())
	}
}
