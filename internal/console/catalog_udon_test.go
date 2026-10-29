package console

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"strings"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
)

func TestRefreshPreservesCatalogWriteFailures(t *testing.T) {
	for _, failedSource := range []string{"udon", "kiva", "wanna"} {
		t.Run(failedSource, func(t *testing.T) {
			c := testConsole(t)
			cfg := fixtureCacheConfig()
			cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, c.settings.VideoDir
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			db, err := sql.Open("sqlite", c.settings.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			keys := map[string]string{"kiva": strings.Repeat("a", 32), "wanna": strings.Repeat("b", 32)}
			trigger := "CREATE TRIGGER reject_catalog_write BEFORE INSERT ON media WHEN NEW.md5='" + keys[failedSource] + "' BEGIN SELECT RAISE(ABORT,'catalog write rejected'); END"
			if failedSource == "udon" {
				trigger = "CREATE TRIGGER reject_catalog_write BEFORE INSERT ON songs WHEN NEW.id=2 BEGIN SELECT RAISE(ABORT,'catalog write rejected'); END"
			}
			if _, err := db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			c.checksumURL = cacheproxy.UdonCatalogURL
			c.client.Transport = catalogTransport(func(r *http.Request) (*http.Response, error) {
				body := monitorNames("20261003000000", "udon").Body
				switch r.URL.Host {
				case "api.udon.dance":
				case "x.kiva.moe":
					body = monitorFull("kiva", "20261002000000", "kiva", keys["kiva"]).Body
				case "wanna.kiva.moe":
					body = monitorFull("wanna", "20261001000000", "wanna", keys["wanna"]).Body
				default:
					t.Errorf("unexpected host %s", r.URL.Host)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
			})
			err = c.refreshLocalCatalog(t.Context(), engine.Store)
			if err == nil || !strings.Contains(err.Error(), "catalog write rejected") {
				t.Fatalf("catalog write failure hidden: %v", err)
			}
			status, err := c.store.CatalogStatus(t.Context())
			wantRevision := "20261003000000"
			if failedSource == "udon" {
				wantRevision = "20261002000000"
			}
			if err != nil || status.Revision != wantRevision || !strings.Contains(status.Error, "catalog write rejected") {
				t.Fatal(status, err)
			}
			var mapping string
			wantMapping := keys["kiva"]
			if failedSource == "kiva" {
				wantMapping = keys["wanna"]
			}
			if err := db.QueryRow("SELECT md5 FROM song_media WHERE song_id=1").Scan(&mapping); err != nil || mapping != wantMapping {
				t.Fatal("successful backup was lost or failed transaction leaked", mapping, err)
			}
		})
	}
}

func TestRefreshUdonAndEqualBackupParticipation(t *testing.T) {
	for _, scenario := range []string{"old-backups", "newer-wanna", "udon-failed", "backups-failed"} {
		t.Run(scenario, func(t *testing.T) {
			c := testConsole(t)
			cfg := fixtureCacheConfig()
			cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, c.settings.VideoDir
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			c.checksumURL = cacheproxy.UdonCatalogURL
			calls := map[string]int{}
			c.client.Transport = catalogTransport(func(r *http.Request) (*http.Response, error) {
				calls[r.URL.Host]++
				body := monitorNames("20261003000000", "udon").Body
				code := 200
				switch r.URL.Host {
				case "api.udon.dance":
					if scenario == "udon-failed" {
						code = 503
					}
				case "x.kiva.moe":
					body = monitorFull("kiva", "20261002000000", "backup", strings.Repeat("a", 32)).Body
					if scenario == "backups-failed" {
						code = 503
					}
				case "wanna.kiva.moe":
					revision := "20261001000000"
					if scenario == "newer-wanna" {
						revision = "20261004000000"
					}
					body = monitorFull("wanna", revision, "wanna", strings.Repeat("b", 32)).Body
					if scenario == "backups-failed" {
						code = 503
					}
				default:
					t.Errorf("unexpected host %s", r.URL.Host)
				}
				return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
			})
			if err := c.refreshLocalCatalog(context.Background(), engine.Store); err != nil {
				t.Fatal(err)
			}
			for _, host := range []string{"api.udon.dance", "x.kiva.moe", "wanna.kiva.moe"} {
				if calls[host] == 0 {
					t.Fatal("source skipped", host, calls)
				}
			}
			status, err := c.store.CatalogStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want := "20261003000000"
			if scenario == "newer-wanna" {
				want = "20261004000000"
			}
			if scenario == "udon-failed" {
				want = "20261002000000"
			}
			if status.Revision != want {
				t.Fatal(status, want)
			}
			// The failure of optional sources never blocks accepted Udon metadata.
			target, release, err := engine.ReadDownloadTarget(context.Background(), cacheproxy.DownloadTarget{SongID: "1"})
			defer release()
			if err != nil || len(target.Songs) != 1 {
				t.Fatal(target, err)
			}
			wantMD5 := strings.Repeat("a", 32)
			if scenario == "newer-wanna" {
				wantMD5 = strings.Repeat("b", 32)
			}
			if scenario == "backups-failed" {
				wantMD5 = ""
			}
			if target.Songs[0].MD5 != wantMD5 {
				t.Fatal(target.Songs[0].MD5, wantMD5)
			}
		})
	}
}
