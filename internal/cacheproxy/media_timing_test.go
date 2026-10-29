package cacheproxy

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestMediaTimingFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		size    int64
		seconds float64
		want    mediaTiming
	}{
		{"known", 60_000_000, 240, mediaTiming{60_000_000, 240 * time.Second}},
		{"duration_only", 0, 240, mediaTiming{60_000_000, 240 * time.Second}},
		{"size_only", 60_000_000, 0, mediaTiming{60_000_000, 240 * time.Second}},
		{"unknown", 0, 0, mediaTiming{typicalSize, typicalDuration}},
		{"invalid", -1, math.NaN(), mediaTiming{typicalSize, typicalDuration}},
		{"infinite", 0, math.Inf(1), mediaTiming{typicalSize, typicalDuration}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := estimateMediaTiming(tc.size, tc.seconds)
			if got != tc.want || got.at(got.size/2) != got.duration/2 || got.at(-1) != 0 || got.at(math.MaxInt64) != got.duration {
				t.Fatal(got, tc.want)
			}
		})
	}
}

func TestMediaTimingUsesMatchingCatalogVersion(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	key, other := "0123456789abcdef0123456789abcdef", "abcdef0123456789abcdef0123456789"
	_, err := s.usage.db.Exec(`INSERT INTO media(md5,byte_size) VALUES (?,60000000),(?,NULL)`, key, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec(`INSERT INTO songs(id,start,end) VALUES (1,50,240),(2,80,300),(3,0,900)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec(`INSERT INTO song_media(song_id,md5) VALUES (1,?),(2,?),(3,?)`, key, key, other); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		song    string
		seconds time.Duration
	}{{"1", 240 * time.Second}, {"2", 300 * time.Second}, {"", 240 * time.Second}, {"3", 240 * time.Second}} {
		got, err := s.mediaTiming(ctx, video{key: key, songID: tc.song, size: 1})
		if err != nil || got.size != 60_000_000 || got.duration != tc.seconds {
			t.Fatal(tc, got, err)
		}
	}
	// No guessed size/duration is written to the database.
	if _, err := s.mediaTiming(ctx, video{key: other}); err != nil {
		t.Fatal(err)
	}
	var size any
	if err := s.usage.db.QueryRow("SELECT byte_size FROM media WHERE md5=?", other).Scan(&size); err != nil || size != nil {
		t.Fatal(size, err)
	}
}
