package cacheproxy

import (
	"fmt"
	"log/slog"
	"os"
	"testing"
)

func publicationTestServer(t testing.TB, songs int) *Server {
	t.Helper()
	store, err := createEmptyFixtureStore(storagePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if songs > 0 {
		if _, err := store.db.Exec(`BEGIN;
WITH RECURSIVE ids(id) AS (SELECT 1 UNION ALL SELECT id+1 FROM ids WHERE id < ?)
INSERT INTO songs(id) SELECT id FROM ids;
INSERT INTO media(md5,byte_size) SELECT printf('%032x',id),10 FROM songs;
INSERT INTO song_media(song_id,md5) SELECT id,printf('%032x',id) FROM songs;
COMMIT;`, songs); err != nil {
			t.Fatal(err)
		}
	}
	return &Server{Store: store, cfg: Config{VideoBudgetBytes: 100, VideoDir: t.TempDir()}}
}

func BenchmarkPublicationAdmissionSpareBudget(b *testing.B) {
	for _, songs := range []int{0, 20000} {
		b.Run(fmt.Sprint(songs), func(b *testing.B) {
			s := publicationTestServer(b, songs)
			v := video{key: videoKey("candidate"), size: 10}
			b.ReportAllocs()
			for b.Loop() {
				r, err := s.reservePublication(b.Context(), v)
				if err != nil {
					b.Fatal(err)
				}
				s.releasePublication(r)
			}
		})
	}
}

func TestPublicationSpareBudgetAllocationsIndependentOfCatalog(t *testing.T) {
	measure := func(t *testing.T, songs int, commit bool) float64 {
		s := publicationTestServer(t, songs)
		// The candidate exactly fills the remaining budget. After committing,
		// its still-live reservation must not be counted a second time.
		const occupant = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		s.retained = map[string]retainedVideo{occupant: {key: occupant, size: 90}}
		s.retainedBytes = 90
		v := video{key: videoKey("candidate"), size: 10}
		return testing.AllocsPerRun(3, func() {
			r, err := s.reservePublication(t.Context(), v)
			if err != nil {
				t.Fatal(err)
			}
			if commit {
				file, err := os.CreateTemp(s.cfg.VideoDir, "publication-*.part")
				if err != nil {
					t.Fatal(err)
				}
				defer os.Remove(file.Name())
				defer file.Close()
				if _, err := file.WriteString("0123456789"); err != nil {
					t.Fatal(err)
				}
				f := &flight{progress: &downloadProgress{log: slog.Default()}}
				if err := s.commitPublication(r, file, s.cfg.videoFile(v.key), v, f); err != nil {
					t.Fatal(err)
				}
			}
			s.releasePublication(r)
		})
	}
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprintf("commit=%t", commit), func(t *testing.T) {
			empty, populated := measure(t, 0, commit), measure(t, 2000, commit)
			// Allow fixed query/runtime overhead, but catch catalog-sized allocations
			// caused by eagerly loading priorities when every publication fits.
			if populated > empty+100 {
				t.Fatalf("spare-budget allocations grow with catalog: empty=%.0f populated=%.0f", empty, populated)
			}
		})
	}
}
