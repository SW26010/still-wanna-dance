package console

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestInventoryRejectsMissingStorageWithRestoredBatchEvidence(t *testing.T) {
	for _, failed := range []bool{false, true} {
		for _, coverage := range []bool{false, true} {
			t.Run(fmt.Sprintf("failed=%t/coverage=%t", failed, coverage), func(t *testing.T) {
				c := testConsole(t)
				if err := c.save(c.settings); err != nil {
					t.Fatal(err)
				}
				var requests atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if failed {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					writeTestMD5Catalog(w, map[int]string{1: "video"})
				}))
				defer upstream.Close()
				c.checksumURL, c.client.Transport = upstream.URL, http.DefaultTransport
				waitMD5Batch(t, c, true)
				if c.batch.Finished.IsZero() || (!failed && c.lastBatch.Total != 1) {
					t.Fatal("batch did not produce usage evidence", c.batch)
				}
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
				root := testStorageRoot(c.settings)
				database, err := os.ReadFile(filepath.Join(root, "storage.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				moved := root + "-unavailable"
				if err := os.Rename(root, moved); err != nil {
					t.Fatal(err)
				}
				c = restartTestConsole(t, c)
				if c.inventory.Videos != 0 || !c.inventory.Updated.IsZero() || c.service != nil || c.batch.Finished.IsZero() {
					t.Fatal("test requires restored task evidence without inventory or engine")
				}
				if !coverage {
					c.checksumURL = ""
				}
				c.client.Transport = http.DefaultTransport
				beforeBatch, beforeLast := c.batch, c.lastBatch
				state := map[string][]byte{}
				for _, kind := range []string{"batch-last-success", "batch-last-attempt", "inventory"} {
					data, err := os.ReadFile(c.files().State(kind))
					if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					state[kind] = data
				}
				beforeRequests := requests.Load()
				c.startInventoryScan()
				got := waitInventory(t, c)
				if got.Error == "" {
					_, statErr := os.Stat(filepath.Join(root, "storage.sqlite"))
					t.Fatalf("missing used root accepted: inventory=%+v recreatedDatabase=%t", got, statErr == nil)
				}
				if !strings.Contains(got.Error, root) || !got.Updated.IsZero() || got.Scanning || got.Videos != 0 {
					t.Fatal("rejection lost diagnostic or changed inventory", got)
				}
				if c.service != nil || requests.Load() != beforeRequests {
					t.Fatal("inventory rejection started media work")
				}
				if !reflect.DeepEqual(c.batch, beforeBatch) || !reflect.DeepEqual(c.lastBatch, beforeLast) {
					t.Fatal("inventory rejection changed restored tasks")
				}
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatal("missing root recreated", err)
				}
				for kind, before := range state {
					after, err := os.ReadFile(c.files().State(kind))
					if (before == nil && !os.IsNotExist(err)) || (before != nil && err != nil) || !bytes.Equal(before, after) {
						t.Fatal("snapshot changed", kind, err)
					}
				}
				if got, err := os.ReadFile(filepath.Join(moved, "storage.sqlite")); err != nil || !bytes.Equal(got, database) {
					t.Fatal("moved database changed", err)
				}
			})
		}
	}
}
