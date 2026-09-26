package console

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanSharedResourceCountsBothSongs(t *testing.T) {
	c := testConsole(t)
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	const body = "shared video"
	if err := os.MkdirAll(filepath.Join(c.settings.StorageDir, "videos"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixtureVideoPath(c.settings.StorageDir, "138", body), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Api/Songs/list" {
			fmt.Fprint(w, `{"groups":{"contents":[{"songInfos":[{"id":138},{"id":140}]}]}}`)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("http://nya.xin.moe/files/2403/138-abc.mp4?e=%x&s=%d", md5.Sum([]byte(body)), len(body)))
		w.WriteHeader(http.StatusFound)
	}))
	defer api.Close()
	defer c.Close()
	c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
	for _, mode := range []struct {
		name             string
		full             bool
		verified, reused int
	}{
		{"first scan", false, 1, 1},
		{"incremental scan", false, 0, 2},
		{"full scan", true, 1, 1},
	} {
		if err := c.startBatchCheck(true, mode.full); err != nil {
			t.Fatal(err)
		}
		c.mu.Lock()
		done := c.batchDone
		c.mu.Unlock()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("scan stalled")
		}
		if c.batch.Hits != 2 || c.batch.Checked != 2 || c.batch.Failed != 0 || c.batch.Missing != 0 || c.service != nil || c.batch.Verified != mode.verified || c.batch.Reused != mode.reused || c.batch.FullVerify != mode.full {
			t.Fatalf("%s: %+v", mode.name, c.batch)
		}
	}
	inventory := scanInventory(c.settings)
	if inventory.Error != "" || inventory.Videos != 1 || inventory.Bytes != int64(len(body)) {
		t.Fatalf("shared resource counted twice: %+v", inventory)
	}
}

func TestScanConcurrencySettings(t *testing.T) {
	c := testConsole(t)
	if c.settings.ScanResolveConcurrency != 4 || c.settings.ScanCheckConcurrency != 1 {
		t.Fatalf("defaults: %+v", c.settings)
	}
	s := c.settings
	s.ScanResolveConcurrency, s.ScanCheckConcurrency = 8, 2
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.settings.ScanResolveConcurrency != 8 || reopened.settings.ScanCheckConcurrency != 2 {
		t.Fatalf("settings not persisted: %+v", reopened.settings)
	}
	for _, pair := range [][2]int{{-1, 1}, {17, 1}, {4, -1}, {4, 5}} {
		s.ScanResolveConcurrency, s.ScanCheckConcurrency = pair[0], pair[1]
		if err := c.save(s); err == nil {
			t.Fatalf("accepted %v", pair)
		}
	}
}

func TestScanLimitsBothStagesAndCancelsWaiters(t *testing.T) {
	for _, limits := range [][2]int{{1, 1}, {3, 2}} {
		t.Run(fmt.Sprint(limits), func(t *testing.T) {
			s := newScanLimiter(Settings{ScanResolveConcurrency: limits[0], ScanCheckConcurrency: limits[1]})
			defer s.ticker.Stop()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resolving, checking := make(chan struct{}, 8), make(chan struct{}, 8)
			release := make(chan struct{})
			done := make(chan error, 8)
			for i := 0; i < 6; i++ {
				go func() {
					_, err := s.check(ctx, func() (string, error) {
						resolving <- struct{}{}
						select {
						case <-release:
							return "video", nil
						case <-ctx.Done():
							return "", ctx.Err()
						}
					}, func(string) (bool, error) {
						checking <- struct{}{}
						<-ctx.Done()
						return false, ctx.Err()
					})
					done <- err
				}()
			}
			await := func(ch <-chan struct{}, n int) {
				t.Helper()
				for i := 0; i < n; i++ {
					select {
					case <-ch:
					case <-time.After(3 * time.Second):
						t.Fatal("concurrency not reached")
					}
				}
				select {
				case <-ch:
					t.Fatal("concurrency limit exceeded")
				case <-time.After(200 * time.Millisecond):
				}
			}
			await(resolving, limits[0])
			close(release)
			await(checking, limits[1])
			cancel()
			for i := 0; i < 6; i++ {
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancel: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("cancellation stalled")
				}
			}
		})
	}
}

func TestScanUsesConfiguredConcurrency(t *testing.T) {
	c := testConsole(t)
	s := c.settings
	s.ScanResolveConcurrency = 2
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Api/Songs/list" {
			fmt.Fprint(w, `{"groups":{"contents":[{"songInfos":[{"id":1},{"id":2},{"id":3}]}]}}`)
			return
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Location", "http://nya.xin.moe/files/2403/"+r.URL.Query().Get("id")+"-abc.mp4?e=d41d8cd98f00b204e9800998ecf8427e&s=1")
		w.WriteHeader(302)
	}))
	defer api.Close()
	defer c.Close()
	c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
	if err := c.startBatchMode(true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("scan did not run concurrently")
		}
	}
	select {
	case <-started:
		t.Fatal("scan exceeded configured concurrency")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	c.mu.Lock()
	done := c.batchDone
	c.mu.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("scan stalled")
	}
	if c.batch.Checked != 3 || c.batch.Missing != 3 || c.batch.Failed != 0 || c.batch.Downloaded != 0 || c.service != nil {
		t.Fatalf("unexpected scan result: %+v", c.batch)
	}
}
