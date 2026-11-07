package console

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
)

func TestRefreshStopsOnCatalogWriteFailure(t *testing.T) {
	for _, failedSource := range []string{"api.udon.dance", "x.kiva.moe", "wanna.kiva.moe"} {
		t.Run(failedSource, func(t *testing.T) {
			c := testConsole(t)
			c.settings.UpstreamMode = "proxy"
			db, err := sql.Open("sqlite", c.settings.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec("CREATE TRIGGER reject_catalog_write BEFORE INSERT ON songs BEGIN SELECT RAISE(ABORT,'catalog write rejected'); END"); err != nil {
				t.Fatal(err)
			}
			c.checksumURL = cacheproxy.UdonCatalogURL
			var accepted atomic.Bool
			c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
				code, body := 503, []byte("")
				if r.URL.Host == failedSource {
					accepted.Store(true)
					code = 200
					body = monitorNames("20261003000000", "udon").Body
					if failedSource != "api.udon.dance" {
						body = monitorFull("kiva", "20261003000000", "backup", strings.Repeat("a", 32)).Body
					}
				}
				return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
			})}
			err = c.refreshLocalCatalog(t.Context(), c.store)
			if !accepted.Load() || !errors.Is(err, cacheproxy.ErrLocalStorage) || !strings.Contains(err.Error(), "catalog write rejected") {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM songs").Scan(&count); err != nil || count != 0 {
				t.Fatal("failed transaction leaked", count, err)
			}
		})
	}
}

func TestRefreshCatalogSourcesRunTogether(t *testing.T) {
	for _, scenario := range []string{"healthy-udon", "http-backup", "invalid-udon", "udon-failed", "kiva-failed", "all-failed", "older-backup"} {
		t.Run(scenario, func(t *testing.T) {
			c := testConsole(t)
			c.settings.UpstreamMode = "proxy"
			c.checksumURL = cacheproxy.UdonCatalogURL
			if scenario == "all-failed" || scenario == "older-backup" {
				revision := "20261001000000"
				if scenario == "older-backup" {
					revision = "20261005000000"
				}
				catalog, err := cacheproxy.ParseCatalog(monitorNames(revision, "local").Body, cacheproxy.UdonCatalogURL)
				if err != nil {
					t.Fatal(err)
				}
				if err := c.store.SyncCatalog(t.Context(), catalog); err != nil {
					t.Fatal(err)
				}
			}
			var calls []string
			var callsMu sync.Mutex
			var started sync.WaitGroup
			started.Add(4)
			c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
				callsMu.Lock()
				calls = append(calls, r.URL.Scheme+"://"+r.URL.Host)
				callsMu.Unlock()
				started.Done()
				started.Wait() // Every source starts before any source can win.
				code, scheme := 200, "https"
				body := monitorFull("kiva", "20261004000000", "backup", strings.Repeat("a", 32)).Body
				switch r.URL.Host {
				case "api.udon.dance":
					body = monitorNames("20261003000000", "udon").Body
					if r.URL.Scheme == "http" {
						scheme = "http"
					}
					if scenario == "invalid-udon" {
						body = []byte("{}")
					} else if scenario != "healthy-udon" && !(scenario == "http-backup" && scheme == "http") {
						code = 503
					}
				case "x.kiva.moe":
					if scenario == "kiva-failed" || scenario == "all-failed" || scenario == "http-backup" {
						code = 503
					}
				case "wanna.kiva.moe":
					if scenario == "all-failed" || scenario == "http-backup" {
						code = 503
					}
				default:
					t.Fatal(r.URL)
				}
				if r.URL.Scheme != scheme {
					t.Fatal("wrong source protocol", r.URL)
				}
				return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
			})}
			err := c.refreshLocalCatalog(t.Context(), c.store)
			if (err != nil) != (scenario == "all-failed") {
				t.Fatal(err)
			}
			wantCalls := []string{"https://api.udon.dance", "http://api.udon.dance", "https://x.kiva.moe", "https://wanna.kiva.moe"}
			wantRevision := "20261003000000"
			if scenario != "healthy-udon" && scenario != "http-backup" {
				wantRevision = "20261004000000"
			}
			slices.Sort(calls)
			slices.Sort(wantCalls)
			if scenario == "all-failed" {
				wantRevision = "20261001000000"
			}
			if scenario == "older-backup" {
				wantRevision = "20261005000000"
			}
			status, err := c.store.CatalogStatus(t.Context())
			if err != nil || !slices.Equal(calls, wantCalls) || status.Revision != wantRevision {
				t.Fatal(calls, status, err)
			}
			db, err := sql.Open("sqlite", c.settings.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var mappings int
			if err := db.QueryRow("SELECT COUNT(*) FROM song_media").Scan(&mappings); err != nil {
				t.Fatal(err)
			}
			if (mappings > 0) != (scenario != "healthy-udon" && scenario != "http-backup" && scenario != "all-failed") {
				t.Fatal("only the selected catalog supplies mappings", mappings)
			}
		})
	}
}
