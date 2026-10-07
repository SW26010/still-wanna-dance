package console

import (
	"context"
	"crypto/md5"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"still-wanna-dance/internal/cacheproxy"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLocalStorageFailureStopsConsoleFallback(t *testing.T) {
	for _, mode := range []string{"observation", "write", "index"} {
		t.Run(mode, func(t *testing.T) {
			c := testConsole(t)
			const body = "local storage fixture"
			var queries, downloads atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); fmt.Fprint(w, body) }))
			defer origin.Close()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				queries.Add(1)
				w.Header().Set("Location", fmt.Sprintf("http://play.udon.dance/files/1/42-video.mp4?e=%x&s=%d", md5.Sum([]byte(body)), len(body)))
				w.WriteHeader(302)
			}))
			defer api.Close()
			c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
			cfg := fixtureCacheConfig()
			cfg.StorageDir = c.settings.StorageDir
			cfg.OriginScheme = "http"
			for host := range cfg.Origins {
				cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
			}
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			c.service = engine
			if mode == "write" {
				dir := filepath.Join(cfg.StorageDir, "tmp")
				if err := os.Remove(dir); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dir, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				db, err := sql.Open("sqlite", filepath.Join(cfg.StorageDir, "stepstash.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				table := "song_urls"
				if mode == "index" {
					table = "media"
				}
				if _, err := db.Exec("CREATE TRIGGER fail_storage BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(FAIL, 'disk failure'); END"); err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range []int64{42, 43, 44} {
				if _, err := c.prefetchSong(context.Background(), engine, id); !errors.Is(err, cacheproxy.ErrLocalStorage) {
					t.Fatal(err)
				}
			}
			if _, err := c.resolvePlayback(context.Background(), "42", "cf", "auto"); !errors.Is(err, cacheproxy.ErrLocalStorage) {
				t.Fatal(err)
			}
			if queries.Load() != 1 {
				t.Fatal("retried API/node/song after local failure", queries.Load())
			}
			want := int32(1)
			if mode == "observation" {
				want = 0
			}
			if downloads.Load() != want {
				t.Fatal("unexpected downloads", downloads.Load())
			}
		})
	}
}
