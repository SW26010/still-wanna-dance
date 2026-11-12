package cacheproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func localBlockPath(t *testing.T, blocks *blockFiles, block int64) string {
	t.Helper()
	blocks.mu.Lock()
	defer blocks.mu.Unlock()
	if blocks.files[block] == nil {
		t.Fatal("missing complete block", block)
	}
	return blocks.files[block].path
}

func TestLocalResourceLifetimePreservesCompleteBlocksAndReaders(t *testing.T) {
	s, cfg := setup(t, nil)
	var first *localResource
	var reader *spoolReader
	var total int64
	for i := range 40 {
		body := "complete block " + strconv.Itoa(i)
		r, _ := http.NewRequest("GET", videoURL(body), nil)
		v, _ := s.parseResolved(r)
		d, err := s.newLocalResource(v, s.cfg.Logger)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.storage.store(0, []byte(body)); err != nil {
			t.Fatal(err)
		}
		total += int64(len(body))
		if i == 0 {
			first = d
			sp := d.makeSpool(nil)
			reader = sp.reader(context.Background(), v.size)
			sp.release()
		}
		s.finishLocalResource(d, context.Canceled)
	}
	defer reader.Close()
	if s.blockCacheBytes.Load() != total {
		t.Fatal("cache without active demands lost its disk charge", s.blockCacheBytes.Load(), total)
	}
	if len(s.localResources) != 40 || s.localResources[first.video.key] == nil {
		t.Fatal("runtime block retention has an arbitrary resource limit")
	}
	wrong := first.video
	wrong.size++
	if _, err := s.acquireLocalResource(wrong); err == nil {
		t.Fatal("same key with a different size reused local blocks")
	}
	path := localBlockPath(t, first.storage, 0)
	s.closeLocalResources()
	if s.blockCacheBytes.Load() != first.video.size {
		t.Fatal("registry cleanup lost the old reader's disk charge", s.blockCacheBytes.Load())
	}
	data := make([]byte, first.video.size)
	if _, err := reader.Read(data); err != nil || string(data) != "complete block 0" {
		t.Fatal("registry cleanup broke an active reader", err)
	}
	reader.Close()
	if s.blockCacheBytes.Load() != 0 {
		t.Fatal("last reader retained its disk charge", s.blockCacheBytes.Load())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("last reader left a block file", err)
	}
	assertNoPartial(t, cfg.tempDir())
}

func TestLateLocalReaderPreservesNewFlightAddress(t *testing.T) {
	body := bytes.Repeat([]byte("x"), int(3*rangeBlockSize))
	oldURL := videoURL(string(body))
	currentURL := strings.Replace(oldURL, "play.udon.dance", "nya.xin.moe", 1) + "&address=current"
	currentURL = strings.Replace(currentURL, "/1344-", "/1345-", 1)
	current, _ := http.NewRequest(http.MethodGet, currentURL, nil)
	started := make(chan int64, 3)
	gate := make(chan struct{}, 1)
	defer close(gate)
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != current.URL.Host || r.URL.Path != current.URL.Path || r.URL.RawQuery != current.URL.RawQuery {
			t.Error("new flight used an obsolete resource address", r.URL)
		}
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		started <- start
		if start == rangeBlockSize {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body[start : end+1])
	})
	s.scheduler.SetSoftLimit(1)
	oldRequest, _ := http.NewRequest(http.MethodGet, oldURL, nil)
	oldVideo, err := s.parseResolved(oldRequest)
	if err != nil {
		t.Fatal(err)
	}
	// Retained blocks can outlive their old flight; a delayed GET may still
	// take its completed/ErrNotRetained branch after the next flight starts.
	local, err := s.newLocalResource(oldVideo, s.cfg.Logger)
	if err != nil {
		t.Fatal(err)
	}
	defer local.storage.release()
	if err := local.storage.store(0, body[:rangeBlockSize]); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := s.Prefetch(ctx, currentURL); finished <- err }()
	if start := receiveDemandEvent(t, started); start != rangeBlockSize {
		t.Fatal("new flight did not reuse the retained first block", start)
	}
	reader, err := s.localBlockReader(ctx, oldVideo)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got := make([]byte, 8)
	if _, err := io.ReadFull(reader, got); err != nil || !bytes.Equal(got, body[:len(got)]) {
		t.Fatal("late reader could not read retained bytes", err)
	}
	// The next missing block reads its route after the old GET acquired data.
	gate <- struct{}{}
	if start := receiveDemandEvent(t, started); start != 2*rangeBlockSize {
		t.Fatal("unexpected next missing block", start)
	}
	if err := receiveDemandEvent(t, finished); err != nil {
		t.Fatal(err)
	}
}

func TestChangedAddressDoesNotReuseStrongValidator(t *testing.T) {
	for _, status := range []int{http.StatusPartialContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			body := bytes.Repeat([]byte("x"), int(2*rangeBlockSize))
			oldURL := videoURL(string(body))
			currentURL := strings.Replace(oldURL, "play.udon.dance", "nya.xin.moe", 1)
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if tag := r.Header.Get("If-Match"); tag != "" && tag != `"current-endpoint"` {
					w.WriteHeader(http.StatusPreconditionFailed)
					return
				}
				w.Header().Set("ETag", `"current-endpoint"`)
				if status == http.StatusOK {
					serveFixtureContent(w, r, string(body))
					return
				}
				var start, end int64
				if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if start > 0 && r.Header.Get("If-Match") != `"current-endpoint"` {
					t.Error("new ranges lost the current endpoint's entity condition")
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(body[start : end+1])
			})
			v := parsedVideo(t, s, string(body))
			local, err := s.newLocalResource(v, s.cfg.Logger)
			if err == nil {
				err = local.acceptEntity(v, `"old-endpoint"`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := local.storage.store(0, body[:rangeBlockSize]); err != nil {
				t.Fatal(err)
			}
			oldPath := localBlockPath(t, local.storage, 0)
			sp := local.makeSpool(nil)
			reader := sp.reader(t.Context(), v.size)
			sp.release()
			s.finishLocalResource(local, context.Canceled) // Registry/reader remain after execution ends.
			defer reader.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if _, err := s.Prefetch(ctx, currentURL); err != nil {
				t.Fatal("same content at the current address failed", err)
			}
			if got, err := os.ReadFile(cfg.videoFile(v.key)); err != nil || !bytes.Equal(got, body) {
				t.Fatal("new entity did not validate and publish", err)
			}
			got := make([]byte, rangeBlockSize)
			if _, err := io.ReadFull(reader, got); err != nil || !bytes.Equal(got, body[:rangeBlockSize]) {
				t.Fatal("changing the download entity broke an old reader", err)
			}
			reader.Close()
			if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
				t.Fatal("old entity survived its last reader", err)
			}
		})
	}
}

func TestChangedRedirectTargetDoesNotKeepOldStrongValidator(t *testing.T) {
	for _, tc := range []struct {
		name                string
		finalEntry, sameTag bool
	}{
		{"alias_changes_target", false, false},
		{"bound_endpoint_redirects", true, false},
		{"same_etag_at_different_endpoint", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const blockSize = 8
			body := "01234567abcdefghABCDEFGH"
			entry := videoURL(body)
			oldURL := strings.Replace(entry, "/1344-", "/1345-", 1)
			newURL := strings.Replace(entry, "/1344-", "/1346-", 1)
			var changed atomic.Bool
			var stale atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/1344-") {
					target := oldURL
					if changed.Load() {
						target = newURL
					}
					http.Redirect(w, r, target, http.StatusTemporaryRedirect)
					return
				}
				if changed.Load() && strings.Contains(r.URL.Path, "/1345-") {
					http.Redirect(w, r, newURL, http.StatusTemporaryRedirect)
					return
				}
				tag := `"old-endpoint"`
				if strings.Contains(r.URL.Path, "/1346-") && !tc.sameTag {
					tag = `"current-endpoint"`
				}
				if condition := r.Header.Get("If-Match"); condition != "" && condition != tag {
					stale.Add(1)
					w.WriteHeader(http.StatusPreconditionFailed)
					return
				}
				w.Header().Set("ETag", tag)
				serveFixtureContent(w, r, body)
			}, func(c *Config) { c.BlockSize = blockSize; c.DownloadGrowInterval = 0 })
			assertResponse(t, request(s, "GET", entry, map[string]string{"Range": "bytes=0-7"}), 206, body[:8])
			s.wg.Wait()
			v := parsedVideo(t, s, body)
			local, err := s.acquireLocalResource(v)
			if err != nil || local == nil {
				t.Fatal("first request did not retain its entity blocks", err)
			}
			oldPath := localBlockPath(t, local.storage, 0)
			sp := local.makeSpool(nil)
			reader := sp.reader(t.Context(), int64(len(body)))
			sp.release()
			local.storage.release()
			defer reader.Close()
			changed.Store(true)
			if tc.finalEntry {
				entry = oldURL
			}
			var statuses []int
			for range 2 {
				response := request(s, "GET", entry, map[string]string{"Range": "bytes=8-15"})
				statuses = append(statuses, response.Code)
				if response.Code == 206 && response.Body.String() != body[8:16] {
					t.Fatal("wrong redirected content")
				}
				s.wg.Wait()
			}
			firstStatus := 206
			if tc.finalEntry {
				firstStatus = 502
			} // Reject the old bound entity before accepting new blocks.
			if statuses[0] != firstStatus || statuses[1] != 206 || stale.Load() != 0 {
				t.Fatalf("changed redirect retained a foreign entity condition: statuses=%v stale=%d", statuses, stale.Load())
			}
			got := make([]byte, blockSize)
			if _, err := io.ReadFull(reader, got); err != nil || string(got) != body[:8] {
				t.Fatal("retiring entity broke old reader", err)
			}
			reader.Close()
			if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
				t.Fatal("old entity blocks were not released", err)
			}
		})
	}
}
