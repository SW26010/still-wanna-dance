package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func receiveDemandEvent[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for demand event")
		var zero T
		return zero
	}
}

func TestQueueDemandPreemptionProtectsGETAndSharedContent(t *testing.T) {
	for _, liveGET := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared-queue", true: "active-get"}[liveGET], func(t *testing.T) {
			started := make(chan string, 16)
			canceled := make(chan string, 16)
			release := make(chan struct{})
			var once sync.Once
			finish := func() { once.Do(func() { close(release) }) }
			defer finish()
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
				key := r.URL.Query().Get("e")
				w.WriteHeader(200)
				io.WriteString(w, "x")
				w.(http.Flusher).Flush()
				started <- key
				select {
				case <-r.Context().Done():
					canceled <- key
				case <-release:
					if key == videoKey("xa") {
						io.WriteString(w, "a")
					} else if key == videoKey("xb") {
						io.WriteString(w, "b")
					} else {
						io.WriteString(w, "c")
					}
				}
			})
			queueCtx := context.WithValue(context.Background(), queueDemandKey{}, true)
			s.UpdatePrefetchQueue([]int64{1, 2, 3}, false)
			start := func(ctx context.Context, id, body string) <-chan error {
				ch := make(chan error, 1)
				go func() { _, err := s.PrefetchSong(ctx, id, videoURL(body)); ch <- err }()
				return ch
			}
			ctxA, cancelA := context.WithCancel(queueCtx)
			defer cancelA()
			doneA := start(ctxA, "1", "xa")
			if receiveDemandEvent(t, started) != videoKey("xa") {
				t.Fatal("wrong first resource")
			}
			ctxB, cancelB := context.WithCancel(queueCtx)
			defer cancelB()
			doneB := start(ctxB, "2", "xb")
			receiveDemandEvent(t, started)
			var sharedDone <-chan error
			var getDone chan struct{}
			var stopGET context.CancelFunc
			if liveGET {
				getCtx, cancel := context.WithCancel(context.Background())
				stopGET = cancel
				defer cancel()
				getDone = make(chan struct{})
				go func() {
					defer close(getDone)
					defer func() {
						if p := recover(); p != nil && p != http.ErrAbortHandler {
							panic(p)
						}
					}()
					r := httptest.NewRequest("GET", videoURL("xa"), nil).WithContext(getCtx)
					s.ServeHTTP(httptest.NewRecorder(), r)
				}()
				deadline := time.Now().Add(3 * time.Second)
				for {
					s.mu.Lock()
					rank := s.resourceRankLocked(videoKey("xa"))
					s.mu.Unlock()
					if rank.level == 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("GET did not register")
					}
					time.Sleep(time.Millisecond)
				}
			} else {
				// A second queue identity attaches to the same worker without a slot.
				sharedDone = start(queueCtx, "3", "xa")
				deadline := time.Now().Add(3 * time.Second)
				for {
					s.mu.Lock()
					key := s.prefetchResources["3"]
					s.mu.Unlock()
					if key == videoKey("xa") {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("shared queue resource did not attach")
					}
					time.Sleep(time.Millisecond)
				}
			}
			cancelA()
			receiveDemandEvent(t, doneA)
			// Keep shared A as the head (or protect it by GET), and make C urgent.
			if liveGET {
				s.UpdatePrefetchQueue([]int64{4, 2}, false)
			} else {
				s.UpdatePrefetchQueue([]int64{3, 4, 2}, false)
			}
			doneC := start(queueCtx, "4", "xc")
			if got := receiveDemandEvent(t, canceled); got != videoKey("xb") {
				t.Fatalf("preempted protected A: %s", got)
			}
			if got := receiveDemandEvent(t, started); got != videoKey("xc") {
				t.Fatalf("new head did not start: %s", got)
			}
			if liveGET {
				select {
				case <-getDone:
					t.Fatal("GET was interrupted by queue update")
				default:
				}
				stopGET()
				receiveDemandEvent(t, getDone)
				// B is still wanted, and can now reclaim A's no-demand slot.
				if got := receiveDemandEvent(t, canceled); got != videoKey("xa") {
					t.Fatalf("GET release did not demote A: %s", got)
				}
				if got := receiveDemandEvent(t, started); got != videoKey("xb") {
					t.Fatal("preempted B did not retry")
				}
			}
			finish()
			if err := receiveDemandEvent(t, doneC); err != nil {
				t.Fatal(err)
			}
			if err := receiveDemandEvent(t, doneB); err != nil {
				t.Fatal(err)
			}
			if sharedDone != nil {
				if err := receiveDemandEvent(t, sharedDone); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			assertNoPartial(t, cfg.tempDir())
		})
	}
}

func videoKey(body string) string {
	r := httptest.NewRequest("GET", videoURL(body), nil)
	return r.URL.Query().Get("e")
}

func TestNoCompetitionAllowsAbandonedDownloadToFinish(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			io.WriteString(w, payload)
		case <-r.Context().Done():
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Prefetch(ctx, videoURL(payload)); done <- err }()
	receiveDemandEvent(t, started)
	cancel()
	if err := receiveDemandEvent(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.mu.Lock()
	f := s.flights[videoKey(payload)]
	s.mu.Unlock()
	close(release)
	receiveDemandEvent(t, f.done)
	if f.err != nil {
		t.Fatal(f.err)
	}
	if data, err := os.ReadFile(cfg.videoFile(videoKey(payload))); err != nil || string(data) != payload {
		t.Fatalf("abandoned download: %s %v", data, err)
	}
	assertNoPartial(t, cfg.tempDir())
}

// A canceled network worker can take time to unwind. The replacement must not
// start until Body.Close and spool cleanup have completed, including same-key GETs.
type demandBody struct {
	ctx      context.Context
	reading  chan struct{}
	canceled chan struct{}
	cleanup  <-chan struct{}
}

func (b *demandBody) Read([]byte) (int, error) {
	close(b.reading)
	<-b.ctx.Done()
	close(b.canceled)
	return 0, b.ctx.Err()
}
func (b *demandBody) Close() error { <-b.cleanup; return nil }

func TestPreemptionWaitsForWorkerCleanup(t *testing.T) {
	s, cfg := setup(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected origin") })
	started := make(chan string, 4)
	canceled, cleanup := make(chan struct{}), make(chan struct{})
	reading := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(cleanup) }) }
	defer finish()
	var mu sync.Mutex
	counts := map[string]int{}
	s.client.Transport = resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
		key := r.URL.Query().Get("e")
		mu.Lock()
		counts[key]++
		n := counts[key]
		mu.Unlock()
		var body io.ReadCloser = io.NopCloser(strings.NewReader("new"))
		if key == videoKey("old") && n == 1 {
			body = &demandBody{r.Context(), reading, canceled, cleanup}
		}
		started <- key
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body, ContentLength: 3, Request: r}, nil
	})
	// Force a single slot, so the GET must preempt the abandoned background task.
	s.cfg.MaxDownloads = 1
	s.slots = make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Prefetch(ctx, videoURL("old")); done <- err }()
	receiveDemandEvent(t, started)
	receiveDemandEvent(t, reading)
	cancel()
	receiveDemandEvent(t, done)
	getDone := make(chan *responseRecorder, 1)
	go func() { getDone <- request(s, "GET", videoURL("new"), nil) }()
	receiveDemandEvent(t, canceled)
	s.mu.Lock()
	occupied, bg := len(s.slots), s.background
	s.mu.Unlock()
	if occupied != 1 || bg != 1 {
		t.Fatalf("capacity released before cleanup: %d %d", occupied, bg)
	}
	select {
	case <-started:
		t.Fatal("replacement started before cleanup")
	default:
	}
	finish()
	if key := receiveDemandEvent(t, started); key != videoKey("new") {
		t.Fatal(key)
	}
	assertResponse(t, receiveDemandEvent(t, getDone), 200, "new")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoPartial(t, cfg.tempDir())
	if _, err := os.Stat(cfg.videoFile(videoKey("old"))); !os.IsNotExist(err) {
		t.Fatal("preempted bytes published", err)
	}
}

func TestGETReattachesAfterSameResourcePreemption(t *testing.T) {
	s, cfg := setup(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected origin") })
	s.cfg.MaxDownloads = 1
	s.slots = make(chan struct{}, 1)
	started := make(chan string, 4)
	reading, canceled, cleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(cleanup) }) }
	defer finish()
	var mu sync.Mutex
	oldCalls := 0
	s.client.Transport = resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
		key := r.URL.Query().Get("e")
		var body io.ReadCloser = io.NopCloser(strings.NewReader("new"))
		if key == videoKey("old") {
			mu.Lock()
			oldCalls++
			call := oldCalls
			mu.Unlock()
			body = io.NopCloser(strings.NewReader("old"))
			if call == 1 {
				body = &demandBody{r.Context(), reading, canceled, cleanup}
			}
		}
		started <- key
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body, ContentLength: 3, Request: r}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldDone := make(chan error, 1)
	go func() { _, err := s.Prefetch(ctx, videoURL("old")); oldDone <- err }()
	receiveDemandEvent(t, started)
	receiveDemandEvent(t, reading)
	cancel()
	receiveDemandEvent(t, oldDone)
	newDone := make(chan error, 1)
	go func() { _, err := s.Prefetch(context.Background(), videoURL("new")); newDone <- err }()
	receiveDemandEvent(t, canceled)
	getDone := make(chan *responseRecorder, 1)
	go func() { getDone <- request(s, "GET", videoURL("old"), nil) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		rank := s.resourceRankLocked(videoKey("old"))
		s.mu.Unlock()
		if rank.level == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("GET did not register while old worker was stopping")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-started:
		t.Fatal("same-key duplicate started before cleanup")
	default:
	}
	finish()
	if got := receiveDemandEvent(t, started); got != videoKey("old") {
		t.Fatalf("P0 lost admission to P3: %s", got)
	}
	assertResponse(t, receiveDemandEvent(t, getDone), 200, "old")
	if got := receiveDemandEvent(t, started); got != videoKey("new") {
		t.Fatal(got)
	}
	if err := receiveDemandEvent(t, newDone); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoPartial(t, cfg.tempDir())
}

func TestHEADAndCompletedRangeDoNotKeepPlaybackDemand(t *testing.T) {
	for _, method := range []string{"HEAD", "GET"} {
		t.Run(method, func(t *testing.T) {
			canceled := make(chan struct{})
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("e") != videoKey(payload) {
					io.WriteString(w, "new")
					return
				}
				io.WriteString(w, payload[:4])
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(canceled)
			})
			s.cfg.MaxDownloads = 1
			s.slots = make(chan struct{}, 1)
			response := request(s, method, videoURL(payload), map[string]string{"Range": "bytes=0-2"})
			if method == "HEAD" {
				assertResponse(t, response, 200, "")
			} else {
				assertResponse(t, response, 206, payload[:3])
			}
			s.mu.Lock()
			rank := s.resourceRankLocked(videoKey(payload))
			s.mu.Unlock()
			if rank != noDemand {
				t.Fatalf("completed %s left demand: %+v", method, rank)
			}
			assertResponse(t, request(s, "GET", videoURL("new"), nil), 200, "new")
			receiveDemandEvent(t, canceled)
		})
	}
}
