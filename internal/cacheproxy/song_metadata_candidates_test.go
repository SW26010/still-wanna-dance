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

	for _, node := range []string{"cf", "nya"} {
		if err := s.ObserveSongURL(context.Background(), PlaybackObservation{
			SongID: id, Node: node,
			URL: fmt.Sprintf("https://play.udon.dance/files/1/%d-video.mp4?e=%s&s=1234", id, key),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSongMetadataCandidatesUseSongPriority(t *testing.T) {
	s, _ := setup(t, nil)
	const a, b, c = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccccccccccc"
	ctx := context.Background()
	if err := s.Store.SyncCatalog(ctx, testCandidate("20261015000000",
		CatalogSong{ID: 101, MD5: a}, CatalogSong{ID: 102, MD5: a}, CatalogSong{ID: 103, MD5: b}, CatalogSong{ID: 104, MD5: c})); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for id, score := range map[int]float64{101: 6, 102: 6, 103: 10, 104: 12} {
		if _, err := s.Store.usage.db.Exec("INSERT INTO song_usage VALUES (?,1,?,?)", id, score, now); err != nil {
			t.Fatal(err)
		}
	}
	assert := func(want []int64) {
		t.Helper()
		got, err := s.Store.SongMetadataCandidates(ctx)
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("candidates=%v, want=%v: %v", got, want, err)
		}
	}
	// A shared resource does not inflate either song's metadata priority.
	assert([]int64{104, 103, 102, 101})
	completeSongMetadata(t, s, 104, c)
	completeSongMetadata(t, s, 101, a)
	assert([]int64{103})
	completeSongMetadata(t, s, 102, a)
	assert([]int64{103})
	completeSongMetadata(t, s, 103, b)
	assert(nil)
}

func TestSongMetadataCandidatesUsePriorAndDecayedDemand(t *testing.T) {
	s, _ := setup(t, nil)
	const a, b = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	ctx := context.Background()
	if err := s.Store.SyncCatalog(ctx, testCandidate("20261015000000", CatalogSong{ID: 1981, MD5: a}, CatalogSong{ID: 5404, MD5: b})); err != nil {
		t.Fatal(err)
	}
	got, err := s.Store.SongMetadataCandidates(ctx)
	if err != nil || !slices.Equal(got, []int64{1981, 5404}) {
		t.Fatal("initial prior order", got, err)
	}
	if err := s.Store.usage.write([]usageEvent{{songID: "1981", at: time.Now().Add(-700 * 24 * time.Hour).UnixMilli(), summaryOnly: true}}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Store.SongMetadataCandidates(ctx)
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
		{"missing_mapping", "DELETE FROM song_media", true},
		{"unknown_size", "UPDATE media SET byte_size=NULL", true},
		{"missing_cf", "DELETE FROM media_urls WHERE node='cf'", true},
		{"missing_nya", "DELETE FROM media_urls WHERE node='nya'", false},
		{"display_node", "UPDATE media_urls SET node='hkg' WHERE node='cf'", true},
		{"old_md5", "INSERT INTO media(md5) VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'); UPDATE media_urls SET md5='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := setup(t, nil)
			ctx := context.Background()
			const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			if err := s.Store.SyncCatalog(ctx, testCandidate("20261015000000", CatalogSong{ID: 42, MD5: key})); err != nil {
				t.Fatal(err)
			}
			completeSongMetadata(t, s, 42, key)
			if tc.mutate != "" {
				if _, err := s.Store.usage.db.Exec(tc.mutate); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.Store.SongMetadataCandidates(ctx)
			var want []int64
			if tc.missing {
				want = []int64{42}
			}
			if err != nil || !slices.Equal(got, want) {
				t.Fatalf("candidates=%v want=%v: %v", got, want, err)
			}
			for _, table := range []string{"song_usage", "request_events"} {
				var count int
				if err := s.Store.usage.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatal("metadata selection created usage", table, count, err)
				}
			}
		})
	}
}
