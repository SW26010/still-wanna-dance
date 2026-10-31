package console

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/vrclog"
)

func TestQueueLatestSnapshotDedupAndRetry(t *testing.T) {
	c := testConsole(t)
	body := "queue video fixture"

	first := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var mu sync.Mutex
	requests := map[string]int{}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body+strings.Split(strings.TrimPrefix(r.URL.Path, "/files/2403/"), "-")[0])
	}))
	defer origin.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		mu.Lock()
		requests[id]++
		mu.Unlock()
		if id == "1" {
			close(first)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if id == "5" {
			http.Error(w, "unavailable", 503)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("http://play.udon.dance/files/2403/%s-abc.mp4?e=%s&s=%d", id, fmt.Sprintf("%x", md5.Sum([]byte(body+id))), len(body+id)))
		w.WriteHeader(302)
	}))
	defer api.Close()
	c.apiBase = api.URL
	c.client.Transport = http.DefaultTransport
	cfg := fixtureCacheConfig()
	cfg.PrepareAhead = 120 * time.Second
	cfg.ResolvePlayback = c.resolvePlayback
	cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
	cfg.Origins["play.udon.dance"] = strings.TrimPrefix(origin.URL, "http://")
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.service = engine
	c.queue = QueueStatus{Running: true, Songs: []vrclog.Song{{ID: 1}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	wake := make(chan struct{}, 1)
	go func() { defer close(done); c.queueWorker(ctx, engine, wake) }()
	defer func() { cancel(); <-done }()
	select {
	case <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("worker not started")
	}
	// Delete the resolving song, reorder the queue, and retain an unsupported
	// entry. Songs 4 and 2 register beyond it; song 5 fails then backs off.
	c.mu.Lock()
	// The failure precedes success: song 4 must not hide song 5's retry error.
	c.setQueueSongsLocked([]vrclog.Song{{ID: -1}, {ID: 5}, {ID: 4}, {ID: 2}}, false)
	c.mu.Unlock()
	once.Do(func() { close(release) })
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.mu.Lock()
		waiting := slices.Contains(c.queue.Active, 4) && slices.Contains(c.queue.Active, 2) && c.queue.Error != ""
		completed := c.queue.Completed
		c.mu.Unlock()
		if waiting {
			if completed != 0 {
				t.Fatal("future queue entry downloaded outside its activation window")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("latest queue was not prepared")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(fixtureVideoPath(testStorageRoot(c.settings), "4", body+"4")); !os.IsNotExist(err) {
		t.Fatal("future queue entry published early", err)
	}
	// The unknown placeholder and failed song each retain a playback duration.
	// Promoting 4 into the window activates its existing demand without another
	// resolution; the retained failure must still remain visible after success.
	c.mu.Lock()
	c.setQueueSongsLocked([]vrclog.Song{{ID: 4}, {ID: -1}, {ID: 5}, {ID: 2}}, false)
	c.mu.Unlock()
	deadline = time.Now().Add(3 * time.Second)
	for {
		c.mu.Lock()
		ready := c.queue.Completed == 1 && c.queue.Error != ""
		c.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not finish latest queue")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for i := 0; i < 5; i++ {
		select {
		case wake <- struct{}{}:
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests["1"] != 1 || requests["2"] != 1 || requests["4"] != 1 || requests["5"] != 1 || len(requests) != 4 {
		t.Fatal(requests)
	}
	if _, err = os.Stat(fixtureVideoPath(testStorageRoot(c.settings), "1", body+"1")); !os.IsNotExist(err) {
		t.Fatal("removed song downloaded", err)
	}
	if _, err = os.Stat(fixtureVideoPath(testStorageRoot(c.settings), "4", body+"4")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(fixtureVideoPath(testStorageRoot(c.settings), "2", body+"2")); !os.IsNotExist(err) {
		t.Fatal("fourth queue entry downloaded outside its activation window", err)
	}
}

func TestQueueLifecycleAndParallelBatch(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.VRChatLogDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if err := c.startBatch(); err != nil {
		t.Fatal("parallel batch rejected", err)
	}
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if c.queue.Running {
		t.Fatal("watcher still running")
	}
	if err := c.start(); err == nil {
		t.Fatal("started after close")
	}
}

func TestQueueErrorsFollowSongsAndRoom(t *testing.T) {
	q := QueueStatus{}
	q.setSongs([]vrclog.Song{{ID: 1}, {ID: 2}, {ID: 3}}, false)
	q.songResult(1, errors.New("first failed"))
	q.songResult(2, nil)
	q.songResult(3, errors.New("third failed"))
	if len(q.Failures) != 2 || !strings.Contains(q.Error, "first failed") || !strings.Contains(q.Error, "third failed") {
		t.Fatalf("lost outstanding errors: %+v", q)
	}
	q.setSongs([]vrclog.Song{{ID: 3}, {ID: 2}, {ID: 1}}, false)
	if q.Failures[0].ID != 3 || q.Failures[1].ID != 1 {
		t.Fatal("failures did not follow reorder", q.Failures)
	}
	q.setSongs([]vrclog.Song{{ID: 3}, {ID: 2}}, false)
	if len(q.Failures) != 1 || strings.Contains(q.Error, "first failed") {
		t.Fatal("removed song retained error", q.Failures)
	}
	q.songResult(1, errors.New("late failure"))
	if len(q.Failures) != 1 || strings.Contains(q.Error, "late failure") {
		t.Fatal("late result restored removed error", q.Failures)
	}
	q.songResult(3, nil)
	if len(q.Failures) != 0 || q.Error != "" {
		t.Fatal("successful retry kept its error", q.Failures)
	}
	q.songResult(3, errors.New("previous room"))
	q.setSongs([]vrclog.Song{{ID: 3}}, true)
	if len(q.Failures) != 0 || q.Error != "" {
		t.Fatal("room reset retained error", q.Failures)
	}
	q.songResult(3, errors.New("cleared queue"))
	q.setSongs(nil, false)
	if len(q.Failures) != 0 || q.Error != "" {
		t.Fatal("empty queue retained error", q.Failures)
	}
}

func TestLiveTailCoalescesSnapshotsBeforeDownload(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.VRChatLogDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.settings.VRChatLogDir, "output_log_2026-09-25_00-00-00.txt")
	snapshot := func(id int) string {
		return fmt.Sprintf("[VideoQueueManager] OnPreSerialization: queue info serialized: [{\"songId\":%d}]\n", id)
	}
	if err := os.WriteFile(path, []byte(snapshot(99)), 0600); err != nil {
		t.Fatal(err)
	}
	requested := make(chan string, 8)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested <- r.URL.Query().Get("id")
		http.Error(w, "fixture", 503)
	}))
	defer api.Close()
	c.apiBase = api.URL
	c.client.Transport = http.DefaultTransport
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(snapshot(1) + "[Behaviour] OnLeftRoom\n" + snapshot(2))
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-requested:
		if id != "2" {
			t.Fatalf("obsolete snapshot fetched: %s", id)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("live tail did not reach worker")
	}
}
