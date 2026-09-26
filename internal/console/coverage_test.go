package console

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCoverageCountsSongsAndPreservesSnapshotOnFailure(t *testing.T) {
	c := testConsole(t)
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(c.settings.StorageDir, "videos"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(c.settings.StorageDir, "stepstash.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE video_versions(version_key TEXT, checksum TEXT, file_bytes INTEGER)`); err != nil {
		t.Fatal(err)
	}
	// a is shared by two songs, b is an old version, c is missing, d truncated.
	for _, ch := range []string{"a", "b", "c", "d"} {
		key, checksum := strings.Repeat(ch, 64), strings.Repeat(ch, 32)
		if _, err := db.Exec(`INSERT INTO video_versions VALUES (?, ?, 4)`, key, checksum); err != nil {
			t.Fatal(err)
		}
		if ch != "c" {
			body := "test"
			if ch == "d" {
				body = "x"
			}
			if err := os.WriteFile(filepath.Join(c.settings.StorageDir, "videos", key+".mp4"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	var calls atomic.Int32
	var fail atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			http.Error(w, "offline", 503)
			return
		}
		fmt.Fprintf(w, `{"code":200,"data":{"time":"revision","groups":[{"entries":[{"id":1,"checksum":%q},{"id":1,"checksum":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},{"id":2,"checksum":%q,"disablePublic":true},{"id":3,"checksum":%q},{"id":4,"checksum":%q},{"id":5,"checksum":%q}]}]}}`, strings.Repeat("a", 32), strings.Repeat("a", 32), strings.Repeat("e", 32), strings.Repeat("c", 32), strings.Repeat("d", 32))
	}))
	defer api.Close()
	c.checksumURL, c.client.Transport = api.URL, http.DefaultTransport
	c.startInventoryScan()
	v := waitInventory(t, c)
	if v.Error != "" || !v.CoverageKnown || v.TotalSongs != 5 || v.CoveredSongs != 2 || calls.Load() != 1 {
		t.Fatalf("%+v calls=%d", v, calls.Load())
	}
	// A restart must retain the coverage, and a failed refresh must not turn it into zero.
	reopened, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.localInventory(); got.CoveredSongs != 2 || !got.CoverageKnown {
		t.Fatalf("lost snapshot: %+v", got)
	}
	fail.Store(true)
	c.startInventoryScan()
	after := waitInventory(t, c)
	if after.Error == "" || after.CoveredSongs != 2 || !after.Updated.Equal(v.Updated) {
		t.Fatalf("lost previous coverage: %+v", after)
	}
}
