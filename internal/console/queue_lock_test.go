package console

import (
	"context"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"stepstash/internal/vrclog"
)

func TestQueueProtectionWaitReleasesStateLockAndPreservesResetOrder(t *testing.T) {
	c := testConsole(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	type update struct {
		ids   []int64
		reset bool
	}
	calls := make(chan update, 2)
	c.queue.protect = func(ids []int64, reset bool) {
		calls <- update{ids, reset}
		if len(ids) > 0 && ids[0] == 1 {
			close(entered)
			<-release
		}
	}
	apply := func(id int64, reset bool, done chan struct{}) {
		c.mu.Lock()
		c.setQueueSongsLocked([]vrclog.Song{{ID: id}}, reset)
		c.mu.Unlock()
		close(done)
	}
	firstDone, secondDone := make(chan struct{}), make(chan struct{})
	go apply(1, false, firstDone)
	<-entered
	go apply(2, true, secondDone)
	statusDone := make(chan int, 1)
	go func() {
		r := httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		statusDone <- w.Code
	}()
	select {
	case code := <-statusDone:
		if code != 200 {
			t.Fatal(code)
		}
	case <-time.After(time.Second):
		t.Fatal("queue protection blocked status")
	}
	c.mu.Lock()
	id := c.queue.Songs[0].ID
	c.mu.Unlock()
	if id != 1 {
		t.Fatal("later reset overtook pending protection", id)
	}
	unblock()
	for _, done := range []chan struct{}{firstDone, secondDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("update stuck")
		}
	}
	got := []update{<-calls, <-calls}
	if !reflect.DeepEqual(got, []update{{[]int64{1}, false}, {[]int64{2}, true}}) {
		t.Fatal(got)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queue.Songs[0].ID != 2 || c.queue.Generation != 1 {
		t.Fatal(c.queue)
	}
}

func TestQueueProtectionWaitAllowsStopCancellation(t *testing.T) {
	c := testConsole(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.queueCancel = cancel
	entered, done := make(chan struct{}), make(chan struct{})
	c.queue.protect = func([]int64, bool) { close(entered); <-ctx.Done() }
	go func() { c.mu.Lock(); c.setQueueSongsLocked([]vrclog.Song{{ID: 1}}, false); c.mu.Unlock(); close(done) }()
	<-entered
	stopped := make(chan int, 1)
	go func() {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/stop", nil)
		r.Header.Set("X-StepStash-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		stopped <- w.Code
	}()
	select {
	case code := <-stopped:
		if code != 200 {
			t.Fatal(code)
		}
	case <-time.After(time.Second):
		t.Fatal("queue wait blocked cancel endpoint")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not end update")
	}
}
