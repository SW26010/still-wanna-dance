package console

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamstate"
)

func TestCatalogFallbackCompletesSilently(t *testing.T) {
	for _, entry := range []string{"inventory", "batch", "background"} {
		t.Run(entry, func(t *testing.T) {
			c := testConsole(t)
			if usable, err := c.syncCatalogs(t.Context(), []upstreamstate.CatalogResponse{monitorNames("20261031000000", "local")}); !usable || err != nil {
				t.Fatal("could not establish existing catalog", usable, err)
			}
			bad := monitorNames("20261102000000", "invalid")
			bad.Body = []byte(`{"invalid":true}`)
			if usable, err := c.syncCatalogs(t.Context(), []upstreamstate.CatalogResponse{bad}); usable || err == nil {
				t.Fatal("failed check was not reported", usable, err)
			}
			c.settings.UpstreamMode = "proxy"
			c.checksumURL = cacheproxy.UdonCatalogURL
			backup := monitorFull("kiva", "20261101000000", "backup", strings.Repeat("a", 32))
			backup.Source = cacheproxy.KivaCatalogURL
			c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
				code, body := http.StatusServiceUnavailable, ""
				if r.URL.Host == "x.kiva.moe" {
					code, body = http.StatusOK, string(backup.Body)
				}
				return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}
			var warning string
			switch entry {
			case "inventory":
				c.startInventoryScan()
				v := waitInventory(t, c)
				if !v.CoverageKnown || v.TotalSongs != 1 || v.Error != "" {
					t.Fatal("backup did not become usable", v)
				}
				warning = c.metadata.CatalogError()
			case "batch":
				waitMD5Batch(t, c, true)
				if c.batch.Total != 1 || c.batch.Checked != 1 {
					t.Fatal("batch did not use backup", c.batch)
				}
				warning = c.batch.CatalogWarning
			case "background":
				if usable, err := c.syncCatalogs(t.Context(), []upstreamstate.CatalogResponse{bad, backup}); !usable || err != nil {
					t.Fatal("usable backup did not complete successfully", usable, err)
				}
				warning = c.metadata.CatalogError()
			}
			status, err := c.store.CatalogStatus(t.Context())
			if err != nil || status.Revision != "20261101000000" || status.Source != backup.Source {
				t.Fatal("backup was not accepted", status, err)
			}
			if warning != "" || c.metadata.CatalogError() != "" {
				t.Fatalf("usable backup should clear previous errors and finish silently: %q / %q", warning, c.metadata.CatalogError())
			}
		})
	}
}

func TestCatalogSuccessDoesNotHideStorageFailure(t *testing.T) {
	c := testConsole(t)
	db, err := sql.Open("sqlite", c.settings.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Udon commits first; the older backup then fails while filling media.
	if _, err := db.Exec("CREATE TRIGGER reject_media_write BEFORE INSERT ON media BEGIN SELECT RAISE(ABORT,'media write rejected'); END"); err != nil {
		t.Fatal(err)
	}
	usable, err := c.syncCatalogs(t.Context(), []upstreamstate.CatalogResponse{
		monitorNames("20261102000000", "primary"),
		monitorFull("kiva", "20261101000000", "backup", strings.Repeat("a", 32)),
	})
	if usable || !errors.Is(err, cacheproxy.ErrLocalStorage) || !strings.Contains(c.metadata.CatalogError(), "media write rejected") {
		t.Fatal("an earlier successful source hid the storage failure", usable, err, c.metadata.CatalogError())
	}
}

func TestBackgroundCatalogRecoveryClearsManualRefreshError(t *testing.T) {
	for _, fileError := range []bool{false, true} {
		name := "catalog-only"
		if fileError {
			name = "independent-file-error"
		}
		t.Run(name, func(t *testing.T) {
			c := testConsole(t)
			good := monitorFull("kiva", "20261101000000", "local", strings.Repeat("a", 32))
			if usable, err := c.syncCatalogs(t.Context(), []upstreamstate.CatalogResponse{good}); !usable || err != nil {
				t.Fatal(usable, err)
			}
			if fileError {
				if err := os.Remove(c.settings.VideoDir); err != nil {
					t.Fatal(err)
				}
			}
			c.settings.UpstreamMode = "proxy"
			c.checksumURL = cacheproxy.UdonCatalogURL
			c.client.Transport = noCandidatesTransport{catalogTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			})}
			c.startInventoryScan()
			before := waitInventory(t, c)
			warning := c.metadata.CatalogError()
			if warning == "" || (!fileError && !before.CoverageKnown) {
				t.Fatal("manual failure was not reported with local data", warning, before)
			}
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			c.syncCatalogs(canceled, []upstreamstate.CatalogResponse{good})
			if c.metadata.CatalogError() != warning {
				t.Fatal("cancellation cleared the failed check")
			}
			c.syncCatalogs(t.Context(), nil)
			if c.metadata.CatalogError() != warning {
				t.Fatal("a check without catalog responses cleared the failed check")
			}
			if usable, err := c.syncCatalogs(t.Context(), []upstreamstate.CatalogResponse{good}); !usable || err != nil {
				t.Fatal(usable, err)
			}
			after := c.localInventory()
			if c.metadata.CatalogError() != "" {
				t.Fatal("successful background check retained sync error")
			}
			if fileError {
				if after.Error == "" || after.Error != before.Error {
					t.Fatal("catalog recovery erased independent file error", before, after)
				}
			} else if after.Error != "" || !after.CoverageKnown || after.TotalSongs != before.TotalSongs {
				t.Fatal("successful background check retained manual refresh error", after)
			}
		})
	}
}
