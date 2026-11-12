package console

import (
	"context"
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/vrclog"
)

func TestQueueInvalidationReplacesSourcesDuringHandoff(t *testing.T) {
	for _, change := range []string{"remove", "room", "reorder"} {
		t.Run(change, func(t *testing.T) {
			c := testConsole(t)
			q := &queueTask{}
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
					serveTestVideo(w, r, body+strings.Split(strings.TrimPrefix(r.URL.Path, "/files/2403/"), "-")[0])
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
			cfg.ResolvePlayback = c.resolvePlayback
			cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
			cfg.Origins["play.udon.dance"] = strings.TrimPrefix(origin.URL, "http://")
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			c.service = engine
			q.setQueueSongs([]vrclog.Song{{ID: 1}, {ID: 2}}, false)
			ctx, cancel := context.WithCancel(context.Background())
			wake := make(chan struct{}, 1)
			done := make(chan struct{})
			go func() { defer close(done); q.queueWorker(ctx, engine, wake) }()
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
			q.mu.Lock()
			next := []vrclog.Song{{ID: 3}}
			if change == "reorder" {
				next = []vrclog.Song{{ID: 3}, {ID: 1}, {ID: 2}}
			}
			updateSources := q.updateSources
			updating, allowUpdate, updated := make(chan struct{}), make(chan struct{}), make(chan struct{})
			finishUpdate := sync.OnceFunc(func() { close(allowUpdate) })
			defer finishUpdate()
			q.updateSources = func(ids []int64, reset bool) {
				if len(ids) > 0 && ids[0] == 3 {
					close(updating)
					<-allowUpdate
				}
				updateSources(ids, reset)
			}
			q.mu.Unlock()
			go func() { q.setQueueSongs(next, false); close(updated) }()
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
			q.mu.Lock()
			old := q.waiters[3]
			q.mu.Unlock()
			songs := []vrclog.Song{{ID: 4}}
			q.setQueueSongs(songs, change == "room")
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
				q.mu.Lock()
				complete := q.Completed == 1 && len(q.waiters) == 0
				failures := len(q.Failures)
				q.mu.Unlock()
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
				if _, err := os.Stat(fixtureVideoPath(filepath.Dir(cfg.VideoDir), id, body+id)); err != nil {
					t.Fatal(id, err)
				}
			}
		})
	}
}
