package cacheproxy

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

const metadataProbeAPI = "https://api.udon.dance/Api/Songs/play"

func completeSongMetadata(t *testing.T, s *Server, id int64, key string) {
	t.Helper()
	at := time.Now().Add(-time.Minute)
	for _, node := range []string{"cf", "nya"} {
		if err := s.ObserveSongURL(context.Background(), SongURL{
			SongID: id, API: metadataProbeAPI, Node: node,
			URL:            fmt.Sprintf("https://play.udon.dance/files/1/%d-video.mp4?e=%s&s=1234", id, key),
			QueryStartedAt: at, ObservedAt: at.Add(time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSongMetadataCandidatesUseRetentionPriority(t *testing.T) {
	s, _ := setup(t, nil)
	const a, b, c = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccccccccccc"
	ctx := context.Background()
	if err := s.SyncCatalog(ctx, testCandidate("20261015000000",
		CatalogSong{ID: 101, MD5: a}, CatalogSong{ID: 102, MD5: a}, CatalogSong{ID: 103, MD5: b}, CatalogSong{ID: 104, MD5: c})); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for id, score := range map[int]float64{101: 6, 102: 6, 103: 10, 104: 12} {
		if _, err := s.usage.db.Exec("INSERT INTO song_usage VALUES (?,1,?,?)", id, score, now); err != nil {
			t.Fatal(err)
		}
	}
	assert := func(want []int64) {
		t.Helper()
		got, err := s.SongMetadataCandidates(ctx, metadataProbeAPI)
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("candidates=%v, want=%v: %v", got, want, err)
		}
	}
	// Shared resource scores add; ties use the retained resource's highest ID,
	// then higher song ID within that resource, just like batch preparation.
	assert([]int64{104, 102, 101, 103})
	completeSongMetadata(t, s, 104, c)
	completeSongMetadata(t, s, 101, a)
	// Complete song 101 still contributes its score to incomplete song 102.
	assert([]int64{102, 103})
	completeSongMetadata(t, s, 102, a)
	assert([]int64{103})
	completeSongMetadata(t, s, 103, b)
	assert(nil)
}

func TestSongMetadataCandidatesUsePriorAndDecayedDemand(t *testing.T) {
	s, _ := setup(t, nil)
	const a, b = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	ctx := context.Background()
	if err := s.SyncCatalog(ctx, testCandidate("20261015000000", CatalogSong{ID: 1981, MD5: a}, CatalogSong{ID: 5404, MD5: b})); err != nil {
		t.Fatal(err)
	}
	got, err := s.SongMetadataCandidates(ctx, metadataProbeAPI)
	if err != nil || !slices.Equal(got, []int64{1981, 5404}) {
		t.Fatal("initial prior order", got, err)
	}
	if err := s.usage.write([]usageEvent{{songID: "1981", at: time.Now().Add(-700 * 24 * time.Hour).UnixMilli(), summaryOnly: true}}); err != nil {
		t.Fatal(err)
	}
	got, err = s.SongMetadataCandidates(ctx, metadataProbeAPI)
	if err != nil || !slices.Equal(got, []int64{5404, 1981}) {
		t.Fatal("real decayed demand must replace prior", got, err)
	}
}

func TestSongMetadataCandidatesDetectMissingCurrentMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, mutate string
		missing      bool
	}{
		{"complete", "", false},
		{"unknown_size", "UPDATE media SET byte_size=NULL", true},
		{"zero_size", "UPDATE media SET byte_size=0", true},
		{"missing_cf", "DELETE FROM song_urls WHERE node='cf'", true},
		{"missing_nya", "DELETE FROM song_urls WHERE node='nya'", false},
		{"empty_url", "UPDATE song_urls SET url='' WHERE node='cf'", true},
		{"failed_url", "UPDATE song_urls SET failed_at=1 WHERE node='cf'", true},
		{"other_api", "UPDATE song_urls SET api='https://other.invalid/play'", true},
		{"display_node", "UPDATE song_urls SET node='hkg' WHERE node='cf'", true},
		{"old_md5", "INSERT INTO media(md5) VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'); UPDATE song_urls SET md5='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'", true},
		{"expired_url", "UPDATE song_urls SET query_started_at=1,observed_at=2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := setup(t, nil)
			ctx := context.Background()
			const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			if err := s.SyncCatalog(ctx, testCandidate("20261015000000", CatalogSong{ID: 42, MD5: key})); err != nil {
				t.Fatal(err)
			}
			completeSongMetadata(t, s, 42, key)
			if tc.mutate != "" {
				if _, err := s.usage.db.Exec(tc.mutate); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.SongMetadataCandidates(ctx, metadataProbeAPI)
			var want []int64
			if tc.missing {
				want = []int64{42}
			}
			if err != nil || !slices.Equal(got, want) {
				t.Fatalf("candidates=%v want=%v: %v", got, want, err)
			}
			for _, table := range []string{"song_usage", "request_events"} {
				var count int
				if err := s.usage.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatal("metadata selection created usage", table, count, err)
				}
			}
		})
	}
}
