package cacheproxy

import (
	"reflect"
	"strings"
	"testing"
)

func TestFirstAvailableMediaURLSkipsInvalidAndDisallowedRows(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := t.Context()
	if _, ok, err := s.Store.FirstAvailableMediaURL(ctx, nil); err != nil || ok {
		t.Fatal("empty store returned an address", ok, err)
	}
	valid := "http://play.udon.dance/files/1/42-video.mp4?e=cccccccccccccccccccccccccccccccc&s=1234"
	if err := s.Store.ObserveSongURL(ctx, PlaybackObservation{SongID: 42, Node: "cf", URL: valid}); err != nil {
		t.Fatal(err)
	}
	// Corrupt or disallowed entries must not hide a later valid CF address.
	for _, row := range []struct{ md5, node, url string }{
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "cf", "invalid"},
		{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "nya", valid},
	} {
		if _, err := s.Store.db.ExecContext(ctx, "INSERT INTO media(md5,byte_size) VALUES (?,1234)", row.md5); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Store.db.ExecContext(ctx, "INSERT INTO media_urls(md5,node,url) VALUES (?,?,?)", row.md5, row.node, row.url); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := s.Store.FirstAvailableMediaURL(ctx, nil)
	if err != nil || !ok || got.URL != valid || got.Node != "cf" {
		t.Fatal("did not select the first valid permitted address", got, ok, err)
	}
}

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
		urls, err := s.Store.SongURLs(t.Context(), id, "cf")
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
	old, err := s.Store.ResourceURLs(t.Context(), b, "cf", 1)
	if err != nil || len(old) != 1 || old[0].URL != address(b, "changed") {
		t.Fatal(old, err)
	}
	all, err := s.Store.AvailableMediaURLs(t.Context())
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
	before, err := s.Store.SongURLs(t.Context(), 42, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []PlaybackObservation{{SongID: 42, URL: o.URL}, {SongID: 42, Node: "cf", URL: "invalid"}, {Node: "cf", URL: o.URL}} {
		if err := s.ObserveSongURL(t.Context(), bad); err == nil {
			t.Fatal("invalid observation accepted")
		}
	}
	after, err := s.Store.SongURLs(t.Context(), 42, "")
	if err != nil || !reflect.DeepEqual(before, after) || s.StorageError() != nil {
		t.Fatal(after, err, s.StorageError())
	}
}
