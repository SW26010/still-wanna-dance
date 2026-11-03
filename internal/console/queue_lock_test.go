package console

import (
	"context"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/vrclog"
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
	c.queue.updateSources = func(ids []int64, reset bool) {
		calls <- update{ids, reset}
		if len(ids) > 0 && ids[0] == 1 {
			close(entered)
			<-release
		}
	}
	apply := func(id int64, reset bool, done chan struct{}) {
		c.setQueueSongs([]vrclog.Song{{ID: id}}, reset)
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
	c.queue.updateSources = func([]int64, bool) { close(entered); <-ctx.Done() }
	go func() { c.setQueueSongs([]vrclog.Song{{ID: 1}}, false); close(done) }()
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- c.Close() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("queue wait blocked application close")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not end update")
	}
}

func TestGatewayStopWaitsForQueueWithdrawal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		withdrawing, allowWithdrawal := make(chan struct{}), make(chan struct{})
		allow := sync.OnceFunc(func() { close(allowWithdrawal) })
		defer allow()
		queueDone := make(chan struct{})
		c := &Console{
			queueDesired: true,
			queueCancel:  cancel,
			queueDone:    queueDone,
			queue: QueueStatus{updateSources: func(ids []int64, reset bool) {
				if len(ids) != 0 || !reset {
					t.Errorf("unexpected withdrawal: %v, reset=%v", ids, reset)
				}
				close(withdrawing)
				<-allowWithdrawal
			}},
		}
		go func() {
			<-ctx.Done()
			c.setQueueSongs(nil, true)
			close(queueDone)
		}()
		stopped := make(chan error, 1)
		go func() { stopped <- c.stop() }()
		<-withdrawing
		synctest.Wait()
		select {
		case err := <-stopped:
			t.Fatalf("stop returned before queue withdrawal: %v", err)
		default:
		}
		if !c.mu.TryLock() {
			t.Fatal("queue withdrawal held the state lock")
		}
		desired := c.queueDesired
		c.mu.Unlock()
		if desired {
			t.Fatal("stop did not clear queue intent before waiting")
		}
		// Gateway startup uses this same lifecycle lock, so it cannot replace
		// the queue before the old queue signals completion.
		if c.lifecycleMu.TryLock() {
			c.lifecycleMu.Unlock()
			t.Fatal("another lifecycle operation could overtake withdrawal")
		}
		allow()
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
	})
}
