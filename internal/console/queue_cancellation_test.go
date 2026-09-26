package console

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"stepstash/internal/cacheproxy"
	"stepstash/internal/vrclog"
)

func TestQueueInvalidationCancelsCapacityWaitButPreservesFlights(t *testing.T) {
	for _, change := range []string{"remove", "room", "outside top three"} {
		t.Run(change, func(t *testing.T) {
			c := testConsole(t)
			body := "queue cancellation video"
			started := make(chan string, 8)
			resolved := make(chan string, 8)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				started <- r.URL.Path
				select {
				case <-release:
					io.WriteString(w, body)
				case <-r.Context().Done():
				}
			}))
			defer origin.Close()
			defer unblock()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := r.URL.Query().Get("id")
				resolved <- id
				w.Header().Set("Location", fmt.Sprintf("http://play.udon.dance/files/2403/%s-abc.mp4?e=%x&s=%d", id, md5.Sum([]byte(body)), len(body)))
				w.WriteHeader(302)
			}))
			defer api.Close()
			c.apiBase = api.URL
			c.client.Transport = http.DefaultTransport
			cfg := cacheproxy.DefaultConfig()
			cfg.OriginScheme = "http"
			cfg.StorageDir = c.settings.StorageDir
			cfg.Origins["play.udon.dance"] = strings.TrimPrefix(origin.URL, "http://")
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			c.service = engine
			c.queue = QueueStatus{Running: true, Songs: []vrclog.Song{{ID: 1}, {ID: 2}}}
			ctx, cancel := context.WithCancel(context.Background())
			wake := make(chan struct{}, 1)
			done := make(chan struct{})
			go func() { defer close(done); c.queueWorker(ctx, engine, wake) }()
			defer func() { cancel(); <-done }()
			take := func(ch <-chan string) string {
				t.Helper()
				select {
				case value := <-ch:
					return value
				case <-time.After(3 * time.Second):
					t.Fatal("worker made no progress")
					return ""
				}
			}
			// Both engine slots remain occupied even after queue callers cancel.
			take(started)
			take(started)
			take(resolved)
			take(resolved)
			c.mu.Lock()
			c.setQueueSongsLocked([]vrclog.Song{{ID: 3}}, false)
			c.mu.Unlock()
			wake <- struct{}{}
			if id := take(resolved); id != "3" {
				t.Fatal(id)
			}
			// Give Prefetch time to enter its capacity wait while both origins block.
			time.Sleep(30 * time.Millisecond)
			c.mu.Lock()
			old := c.queue.waiters[3]
			songs := []vrclog.Song{{ID: 4}}
			if change == "outside top three" {
				songs = []vrclog.Song{{ID: 4}, {ID: -1}, {ID: -1}, {ID: 3}}
			}
			c.setQueueSongsLocked(songs, change == "room")
			c.mu.Unlock()
			if old == nil || old.ctx.Err() != context.Canceled {
				t.Fatal("obsolete waiter was not canceled with queue update")
			}
			select {
			case wake <- struct{}{}:
			default:
			}
			if id := take(resolved); id != "4" {
				t.Fatalf("new queue blocked behind obsolete waiter: %s", id)
			}
			unblock()
			if path := take(started); !strings.Contains(path, "/4-") {
				t.Fatalf("obsolete song downloaded: %s", path)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				c.mu.Lock()
				complete := c.queue.Completed == 1 && len(c.queue.waiters) == 0
				failures := len(c.queue.Failures)
				c.mu.Unlock()
				if complete {
					if failures != 0 {
						t.Fatal("cancellation recorded as failure")
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("new queue did not finish")
				}
				time.Sleep(5 * time.Millisecond)
			}
			// These old songs had already started: canceling their waiters must
			// preserve the shared flights and their verified published files.
			for _, id := range []string{"1", "2", "4"} {
				if _, err := os.Stat(fixtureVideoPath(cfg.StorageDir, id, body)); err != nil {
					t.Fatal(id, err)
				}
			}
			if _, err := os.Stat(fixtureVideoPath(cfg.StorageDir, "3", body)); !os.IsNotExist(err) {
				t.Fatal("obsolete cache published", err)
			}
		})
	}
}
