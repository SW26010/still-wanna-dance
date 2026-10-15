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

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/vrclog"
)

func TestQueueInvalidationReplacesSourcesDuringHandoff(t *testing.T) {
	for _, change := range []string{"remove", "room", "outside top three", "reorder"} {
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
					io.WriteString(w, body+strings.Split(strings.TrimPrefix(r.URL.Path, "/files/2403/"), "-")[0])
				case <-r.Context().Done():
				}
			}))
			defer origin.Close()
			defer unblock()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := r.URL.Query().Get("id")
				resolved <- id
				w.Header().Set("Location", fmt.Sprintf("http://play.udon.dance/files/2403/%s-abc.mp4?e=%x&s=%d", id, md5.Sum([]byte(body+r.URL.Query().Get("id"))), len(body+r.URL.Query().Get("id"))))
				w.WriteHeader(302)
			}))
			defer api.Close()
			c.apiBase = api.URL
			c.client.Transport = http.DefaultTransport
			cfg := fixtureCacheConfig()
			cfg.ResolvePlayback = func(ctx context.Context, id, node string) (string, error) {
				return c.resolvePlayback(ctx, id, node, "auto")
			}
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
			// Establish both old sources before replacing the queue.
			take(started)
			take(started)
			take(resolved)
			take(resolved)
			c.mu.Lock()
			next := []vrclog.Song{{ID: 3}}
			if change == "reorder" {
				next = []vrclog.Song{{ID: 3}, {ID: 1}, {ID: 2}}
			}
			protect := c.queue.protect
			updating, allowUpdate, updated := make(chan struct{}), make(chan struct{}), make(chan struct{})
			finishUpdate := sync.OnceFunc(func() { close(allowUpdate) })
			defer finishUpdate()
			c.queue.protect = func(ids []int64, reset bool) {
				if len(ids) > 0 && ids[0] == 3 {
					close(updating)
					<-allowUpdate
				}
				protect(ids, reset)
			}
			c.mu.Unlock()
			go func() { c.mu.Lock(); c.setQueueSongsLocked(next, false); c.mu.Unlock(); close(updated) }()
			<-updating
			select {
			case wake <- struct{}{}:
			default:
			}
			select {
			case id := <-resolved:
				t.Fatalf("resolved %s before queue source update finished", id)
			case <-time.After(50 * time.Millisecond):
			}
			finishUpdate()
			<-updated
			wake <- struct{}{}
			if id := take(resolved); id != "3" {
				t.Fatal(id)
			}
			// Resolution/caller replacement must progress immediately. Network work
			// keeps a finite handoff and starts according to block deadlines.
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
			// Only the newest head must publish. Uncontended old work may finish;
			// preempted work must not publish a partial file.
			for _, id := range []string{"4"} {
				if _, err := os.Stat(fixtureVideoPath(cfg.StorageDir, id, body+id)); err != nil {
					t.Fatal(id, err)
				}
			}
		})
	}
}
