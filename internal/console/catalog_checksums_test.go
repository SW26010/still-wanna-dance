package console

import (
	"crypto/md5"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestChecksumScanFastPathAndFallback(t *testing.T) {
	for _, mode := range []string{"reuse", "full", "revision", "changed", "invalid", "duplicate", "unavailable", "unmapped", "missing", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			c := testConsole(t)
			if err := c.save(c.settings); err != nil {
				t.Fatal(err)
			}
			const body = "catalog scan fixture"
			digest := fmt.Sprintf("%x", md5.Sum([]byte(body)))
			path := fixtureVideoPath(c.settings.StorageDir, "138", body)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if mode != "missing" {
				data := body
				if mode == "corrupt" {
					data = strings.Repeat("x", len(body))
				}
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			db, err := sql.Open("sqlite", filepath.Join(c.settings.StorageDir, "stepstash.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`CREATE TABLE current_videos(song_id TEXT, version_key TEXT); CREATE TABLE video_versions(version_key TEXT, checksum TEXT, file_bytes INTEGER, source_path TEXT)`)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "unmapped" {
				key := strings.TrimSuffix(filepath.Base(path), ".mp4")
				if _, err := db.Exec(`INSERT INTO current_videos VALUES ('138', ?);`, key); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO video_versions VALUES (?, ?, ?, '/files/2403/138-abc.mp4')`, key, digest, len(body)); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			var resolves atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Api/Songs/list":
					fmt.Fprint(w, `{"time":"revision1","groups":{"contents":[{"songInfos":[{"id":138}]}]}}`)
				case "/checksums":
					if mode == "unavailable" {
						http.Error(w, "offline", 503)
						return
					}
					stamp, checksum := "revision1", digest
					if mode == "revision" {
						stamp = "older"
					}
					if mode == "changed" {
						checksum = strings.Repeat("a", 32)
					}
					if mode == "invalid" {
						checksum = "bad"
					}
					entry := fmt.Sprintf(`{"id":138,"checksum":%q}`, checksum)
					if mode == "duplicate" {
						entry += "," + entry
					}
					fmt.Fprintf(w, `{"code":200,"data":{"time":%q,"groups":[{"entries":[%s]}]}}`, stamp, entry)
				default:
					resolves.Add(1)
					w.Header().Set("Location", fmt.Sprintf("https://nya.xin.moe/files/2403/138-abc.mp4?e=%s&s=%d", digest, len(body)))
					w.WriteHeader(302)
				}
			}))
			defer api.Close()
			c.apiBase, c.checksumURL, c.client.Transport = api.URL, api.URL+"/checksums", http.DefaultTransport
			for pass := 0; pass < 2; pass++ {
				if err := c.startBatchCheck(true, mode == "full"); err != nil {
					t.Fatal(err)
				}
				c.mu.Lock()
				done := c.batchDone
				c.mu.Unlock()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("scan stalled")
				}
				if c.batch.Failed != 0 || c.batch.Checked != 1 {
					t.Fatalf("%+v", c.batch)
				}
			}
			want := int32(2)
			if mode == "reuse" || mode == "full" {
				want = 0
			}
			if want == 0 && (c.batch.CatalogHits != 1 || !c.scanPlan.results[138].localOnly) {
				t.Fatal("missing catalog fast-path receipt")
			}
			if resolves.Load() != want {
				t.Fatalf("playback requests=%d want=%d", resolves.Load(), want)
			}
			if mode == "reuse" && c.batch.Reused != 1 {
				t.Fatalf("not reused: %+v", c.batch)
			}
			if mode == "full" && c.batch.Verified != 1 {
				t.Fatalf("full scan skipped hash: %+v", c.batch)
			}
			if (mode == "missing" || mode == "corrupt") && c.batch.Missing != 1 {
				t.Fatalf("bad file accepted: %+v", c.batch)
			}
		})
	}
}
