package console

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"stepstash/internal/cacheproxy"
)

func TestTrafficWithoutRunningCDN(t *testing.T) {
	c := testConsole(t)
	cfg := cacheproxy.DefaultConfig()
	cfg.StorageDir = c.settings.StorageDir
	s, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", filepath.Join(cfg.StorageDir, "stepstash.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE traffic_totals SET hits=3,saved_bytes=123 WHERE id=1`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	read := func() cacheproxy.TrafficStats {
		t.Helper()
		w := httptest.NewRecorder()
		c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
		var result struct {
			Running bool
			Traffic cacheproxy.TrafficStats
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Running {
			t.Fatal("status must not start CDN")
		}
		return result.Traffic
	}
	if v := read(); v.Error != "" || v.Hits != 3 || v.SavedBytes != 123 {
		t.Fatalf("persisted: %+v", v)
	}
	c.settings.StorageDir = t.TempDir()
	if v := read(); v.Error != "" || v.Requests != 0 {
		t.Fatalf("changed directory: %+v", v)
	}
}
