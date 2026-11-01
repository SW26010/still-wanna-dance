package console

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestProductionBootstrapSeedsMediaWithoutCatalogOrEngine(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	for i := 0; i < 2; i++ {
		c, err := New(config, "127.0.0.1:0", testWorkDirectory(t))
		if err != nil {
			t.Fatal(err)
		}
		keys, err := c.store.KnownMedia(t.Context())
		if err != nil || len(keys) != 10446 {
			c.Close()
			t.Fatal(len(keys), err)
		}
		status, err := c.store.CatalogStatus(t.Context())
		if err != nil || status.Revision != "" || c.service != nil {
			c.Close()
			t.Fatal(status, err)
		}
		db, err := sql.Open("sqlite", c.settings.DatabasePath)
		if err != nil {
			c.Close()
			t.Fatal(err)
		}
		var count int
		err = db.QueryRow("SELECT count(*) FROM catalog_state").Scan(&count)
		db.Close()
		if err != nil || count != 0 {
			c.Close()
			t.Fatal(count, err)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
