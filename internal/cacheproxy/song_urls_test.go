package cacheproxy

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"
)

func TestSongAndResourceURLsShareValidityRules(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := t.Context()
	const key = "0123456789abcdef0123456789abcdef"
	now := time.Now().Add(-time.Minute)
	for i, name := range []string{"expired", "old", "latest", "other-node", "failed", "invalid", "future"} {
		at := now.Add(time.Duration(i) * time.Second)
		o := SongURL{SongID: 42, API: "https://api.udon.dance/" + name, Node: "cf", URL: "https://play.udon.dance/files/1/42-video.mp4?e=" + key + "&s=12", QueryStartedAt: at, ObservedAt: at}
		switch name {
		case "other-node":
			o.Node = "nya"
		case "expired":
			o.QueryStartedAt, o.ObservedAt = now.Add(-25*time.Hour), now.Add(-25*time.Hour)
		case "future":
			o.QueryStartedAt, o.ObservedAt = now.Add(time.Hour), now.Add(time.Hour)
		}
		if err := s.ObserveSongURL(ctx, o); err != nil {
			t.Fatal(err)
		}
		if name == "failed" {
			if err := s.RejectSongURL(ctx, o); err != nil {
				t.Fatal(err)
			}
		}
		if name == "invalid" {
			if _, err := s.db.Exec("UPDATE song_urls SET url='https://evil.invalid/file' WHERE api=?", o.API); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, node := range []string{"", "cf", "nya"} {
		songs, err := s.SongURLs(ctx, 42, node)
		if err != nil {
			t.Fatal(err)
		}
		resources, err := s.ResourceURLs(ctx, key, node, 32)
		if err != nil || !reflect.DeepEqual(songs, resources) {
			t.Fatal("song/resource validity diverged", node, songs, resources, err)
		}
		want := map[string]int{"": 3, "cf": 2, "nya": 1}[node]
		if len(resources) != want {
			t.Fatal(node, resources)
		}
		for i, o := range resources {
			if o.MD5 != key || o.Size != 12 || o.ObservedAt.Location() != time.UTC || o.QueryStartedAt.Location() != time.UTC || i > 0 && o.QueryStartedAt.After(resources[i-1].QueryStartedAt) {
				t.Fatal("incorrect decoding/order", resources)
			}
		}
	}
	recent, err := s.RecentSongURLs(ctx)
	eligible, eligibleErr := s.SongURLs(ctx, 42, "cf")
	if err != nil || eligibleErr != nil || !reflect.DeepEqual(recent, eligible) {
		t.Fatal("cross-song observations changed validity or source policy", err, eligibleErr)
	}
	// More recent evidence for another resource cannot consume the fixed-MD5 budget.
	other := SongURL{SongID: 43, API: "https://api.udon.dance/other-resource", Node: "cf", URL: "https://play.udon.dance/files/1/43-video.mp4?e=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&s=12", QueryStartedAt: now.Add(10 * time.Second), ObservedAt: now.Add(10 * time.Second)}
	if err := s.ObserveSongURL(ctx, other); err != nil {
		t.Fatal(err)
	}
	got, err := s.ResourceURLs(ctx, key, "cf", 2)
	// The invalid newest row still counts toward the bounded database scan.
	if err != nil || len(got) != 1 || got[0].API != "https://api.udon.dance/latest" {
		t.Fatal("fixed resource ordering/budget", got, err)
	}
}

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
	reopenFixtureStore(t, &cfg)
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

func TestSongURLReplacesBackupMapping(t *testing.T) {
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
	if err := s.usage.db.QueryRow("SELECT md5 FROM song_media WHERE song_id=42").Scan(&mapped); err != nil || mapped != "0123456789abcdef0123456789abcdef" {
		t.Fatal(mapped, err)
	}
	var size sql.NullInt64
	if err := s.usage.db.QueryRow("SELECT byte_size FROM media WHERE md5=?", expected).Scan(&size); err != nil || size.Valid {
		t.Fatal("conflicting URL changed the mapped resource", size, err)
	}
	if err := s.usage.db.QueryRow("SELECT byte_size FROM media WHERE md5=?", "0123456789abcdef0123456789abcdef").Scan(&size); err != nil || !size.Valid || size.Int64 != 12 {
		t.Fatal("conflicting URL did not record its own resource size", size, err)
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

func TestSongURLNanosecondTiesAndClockRollback(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	at := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	o := SongURL{SongID: 42, API: "https://api.udon.dance/Api/Songs/play", Node: "cf", URL: "https://play.udon.dance/files/1/42-video.mp4?e=0123456789abcdef0123456789abcdef&s=12", QueryStartedAt: at, ObservedAt: at.Add(time.Second)}
	if err := s.ObserveSongURL(ctx, o); err != nil {
		t.Fatal(err)
	}
	newer := o
	newer.QueryStartedAt = at.Add(time.Nanosecond).In(time.FixedZone("east", 8*3600))
	newer.URL += "&version=new"
	if err := s.ObserveSongURL(ctx, newer); err != nil {
		t.Fatal(err)
	}
	o.QueryStartedAt = newer.QueryStartedAt
	if err := s.ObserveSongURL(ctx, o); err != nil {
		t.Fatal(err)
	}
	got, err := s.SongURLs(ctx, 42, "cf")
	if err != nil || len(got) != 1 || got[0].URL != newer.URL || got[0].QueryStartedAt.Location() != time.UTC {
		t.Fatal(got, err)
	}
	if _, err := s.usage.db.Exec("UPDATE song_urls SET observed_at=?", time.Now().Add(time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
	got, err = s.SongURLs(ctx, 42, "cf")
	if err != nil || len(got) != 0 {
		t.Fatal("clock rollback reused future evidence", got, err)
	}
	o.QueryStartedAt = time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC)
	o.ObservedAt = o.QueryStartedAt.Add(time.Second)
	if err := s.ObserveSongURL(ctx, o); err == nil {
		t.Fatal("overflowing nanoseconds accepted")
	}
}
