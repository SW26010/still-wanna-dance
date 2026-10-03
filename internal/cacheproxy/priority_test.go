package cacheproxy

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	initialpriority "still-wanna-dance/data/initial-priority"
)

func seedPrioritySong(t *testing.T, s *Server, id, key string) {
	t.Helper()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT OR IGNORE INTO songs(song_id) VALUES (?)`, []any{id}},
		{`INSERT OR IGNORE INTO video_versions(version_key,checksum,file_bytes,source_path) VALUES (?,'checksum',10,'/fixture')`, []any{key}},
		{`INSERT INTO song_videos(song_id,version_key) VALUES (?,?)`, []any{id, key}},
	} {
		if _, err := s.usage.db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEffectivePriorityReplacesPriorAcrossVersions(t *testing.T) {
	s, _ := setup(t, nil)
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64)
	seedPrioritySong(t, s, "1981", first)
	seedPrioritySong(t, s, "1981", second)
	now := time.Now().UnixMilli()
	scores, err := s.songPriorities(context.Background(), []int64{1981, 999999}, now)
	if err != nil || scores[1981] != initialpriority.Score(1981) || scores[999999] != 0 {
		t.Fatalf("%v %v", scores, err)
	}
	old := now - (700 * 24 * time.Hour).Milliseconds()
	if err := s.usage.write([]usageEvent{{id: first, at: old, summaryOnly: true}, {id: second, at: old, summaryOnly: true}}); err != nil {
		t.Fatal(err)
	}
	scores, err = s.songPriorities(context.Background(), []int64{1981}, now)
	want := retentionScore(2, old, now)
	if err != nil || math.Abs(scores[1981]-want) > 1e-10 || scores[1981] >= initialpriority.Score(1981) {
		t.Fatalf("prior not replaced: %v %v", scores, err)
	}
}

func TestRetentionUsesEffectiveSongPriority(t *testing.T) {
	for _, mode := range []string{"initial", "old-demand", "fresh-demand", "shared"} {
		t.Run(mode, func(t *testing.T) {
			s, cfg := setup(t, nil)
			a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
			seedPrioritySong(t, s, "1981", a)
			seedPrioritySong(t, s, "5404", b)
			if mode == "shared" {
				seedPrioritySong(t, s, "999999", a)
			}
			if mode == "old-demand" || mode == "fresh-demand" {
				at := time.Now().UnixMilli()
				if mode == "old-demand" {
					at -= (700 * 24 * time.Hour).Milliseconds()
				}
				if err := s.usage.write([]usageEvent{{id: a, at: at, summaryOnly: true}}); err != nil {
					t.Fatal(err)
				}
			}
			putRetained(t, cfg.videoFile(a), 10)
			putRetained(t, cfg.videoFile(b), 10)
			setRetentionLimit(t, s, 10)
			if err := s.trimCachePass(false); err != nil {
				t.Fatal(err)
			}
			expectRetained(t, cfg.videoFile(a), mode != "old-demand")
			expectRetained(t, cfg.videoFile(b), mode == "old-demand")
		})
	}
}
