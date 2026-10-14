package cacheproxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func checkResourceLevel(t *testing.T, s *Server, key string, level int) {
	t.Helper()
	s.mu.Lock()
	rank := s.resourceRankLocked(key)
	s.mu.Unlock()
	if rank.level != level {
		t.Fatalf("resource %s: level = %d, want %d", key, rank.level, level)
	}
}

func TestPlaybackGraceSharedGETReconnectAndExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Server{capacityChanged: make(chan struct{}), flights: map[string]*flight{"shared": {}}}
		ctx, cancel := context.WithCancel(context.Background())
		_, release1 := s.addDemand(ctx, video{key: "shared"}, true)
		_, release2 := s.addDemand(context.Background(), video{key: "shared"}, true)
		cancel()
		synctest.Wait()
		checkResourceLevel(t, s, "shared", 0)
		time.Sleep(2 * time.Second)
		checkResourceLevel(t, s, "shared", 0)
		release2()
		checkResourceLevel(t, s, "shared", 1)
		priority := s.resourceRequestContext(context.Background(), "shared").Value(requestPriorityKey{}).(requestPriority)
		if urgent, _ := priority(); urgent {
			t.Fatal("grace retained the live GET HTTP priority")
		}
		time.Sleep(600 * time.Millisecond)
		_, release3 := s.addDemand(context.Background(), video{key: "shared"}, true)
		checkResourceLevel(t, s, "shared", 0)
		if urgent, _ := priority(); !urgent {
			t.Fatal("reconnect did not regain HTTP priority")
		}
		time.Sleep(600 * time.Millisecond) // past the first grace deadline
		synctest.Wait()
		checkResourceLevel(t, s, "shared", 0)
		release3()
		release1() // a late/idempotent old release must not affect the new period
		checkResourceLevel(t, s, "shared", 1)
		s.mu.Lock()
		changed := s.capacityChanged
		s.mu.Unlock()
		time.Sleep(time.Second - time.Nanosecond)
		checkResourceLevel(t, s, "shared", 1)
		select {
		case <-changed:
			t.Fatal("grace expired early")
		default:
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		// Check notification before asking for a rank: expiry must wake waiters
		// without a new request or a polling resourceRankLocked call.
		select {
		case <-changed:
		default:
			t.Fatal("grace expiry did not wake existing waiters")
		}
		checkResourceLevel(t, s, "shared", priorityNone)
		if len(s.playbackGrace) != 0 {
			t.Fatal("expired resource state retained")
		}
	})
}

func TestPlaybackGraceCancellationBeforeCallbackAndQueueChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Server{capacityChanged: make(chan struct{}), flights: map[string]*flight{"shared": {}}}
		s.UpdatePrefetchQueue([]int64{1, 2}, false)
		queue := context.WithValue(context.Background(), queueDemandKey{}, true)
		_, releaseQueue := s.addDemand(queue, video{key: "shared", songID: "1"}, false)
		defer releaseQueue()
		ctx, cancel := context.WithCancel(context.Background())
		_, release := s.addDemand(ctx, video{key: "shared"}, true)
		s.mu.Lock()
		cancel() // the asynchronous release cannot acquire mu yet
		rank := s.resourceRankLocked("shared")
		s.mu.Unlock()
		if rank.level != 1 {
			t.Fatalf("canceled GET briefly exposed queue priority: %+v", rank)
		}
		synctest.Wait()
		s.UpdatePrefetchQueue(nil, true)
		checkResourceLevel(t, s, "shared", 1)
		time.Sleep(600 * time.Millisecond)
		s.UpdatePrefetchQueue([]int64{2, 1}, false)
		checkResourceLevel(t, s, "shared", 1)
		release()
		time.Sleep(400 * time.Millisecond)
		synctest.Wait()
		checkResourceLevel(t, s, "shared", 3) // latest queue order, not the old head
	})
}

func TestPlaybackGraceOnlyProtectsExistingDownload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, name := range []string{"no-download", "already-canceled", "completed-flight"} {
			s := &Server{capacityChanged: make(chan struct{})}
			if name == "completed-flight" {
				s.flights = map[string]*flight{name: {completed: true}}
			}
			ctx, cancel := context.WithCancel(context.Background())
			if name == "already-canceled" {
				cancel()
			}
			_, release := s.addDemand(ctx, video{key: name}, true)
			release()
			cancel()
			checkResourceLevel(t, s, name, priorityNone)
			if len(s.playbackGrace) != 0 {
				t.Fatalf("%s created a grace without an unfinished download", name)
			}
		}
		synctest.Wait()
	})
}

func TestPlaybackGraceNotRetainedAfterDownloadCompletion(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payload))
	})
	for range 2 { // cold completion, then a verified local hit
		assertResponse(t, request(s, "GET", videoURL(payload), nil), http.StatusOK, payload)
		checkResourceLevel(t, s, videoKey(payload), priorityNone)
		s.mu.Lock()
		count := len(s.playbackGrace)
		s.mu.Unlock()
		if count != 0 {
			t.Fatal("completed cache retained a playback timer")
		}
	}
}

func TestPlaybackGracePrefersQueueVictimButYieldsToLiveGET(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, queueCompetitor := range []bool{true, false} {
			s := &Server{capacityChanged: make(chan struct{}), flights: make(map[string]*flight)}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			old, cancelOld := context.WithCancelCause(context.Background())
			defer cancelOld(nil)
			other, cancelOther := context.WithCancelCause(context.Background())
			defer cancelOther(nil)
			// The recent download is older: a same-rank policy would pick it.
			s.flights["recent"] = &flight{id: 1, cancel: cancelOld, log: log}
			_, release := s.addDemand(context.Background(), video{key: "recent"}, true)
			release()
			if queueCompetitor {
				s.flights["head"] = &flight{id: 2, cancel: cancelOther, log: log}
				s.UpdatePrefetchQueue([]int64{1}, false)
				queue := context.WithValue(context.Background(), queueDemandKey{}, true)
				_, releaseQueue := s.addDemand(queue, video{key: "head", songID: "1"}, false)
				defer releaseQueue()
			}
			s.mu.Lock()
			queuePreempted := s.preemptLocked("head-waiter", downloadRank{level: priorityQueueHead}, false)
			preempted := s.preemptLocked("new-get", downloadRank{level: priorityPlayback}, false)
			s.mu.Unlock()
			if queuePreempted {
				t.Fatal("queue head preempted grace")
			}
			if !preempted {
				t.Fatal("live GET could not acquire a victim")
			}
			if queueCompetitor {
				if old.Err() != nil || !errors.Is(context.Cause(other), errPreempted) {
					t.Fatal("did not prefer the queue victim over recent playback")
				}
			} else if !errors.Is(context.Cause(old), errPreempted) {
				t.Fatal("grace incorrectly became unpreemptible P0")
			}
		}
	})
}

func TestPlaybackGraceOrdersRealWaitersWithoutResurrectingCanceledOnes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Server{capacityChanged: make(chan struct{}), flights: map[string]*flight{"recent": {}}}
		s.UpdatePrefetchQueue([]int64{1, 2}, false)
		queue := context.WithValue(context.Background(), queueDemandKey{}, true)
		ctx, cancel := context.WithCancel(queue)
		d, release := s.addDemand(ctx, video{key: "recent", songID: "2"}, false)
		defer release()
		_, releaseGET := s.addDemand(context.Background(), video{key: "recent"}, true)
		releaseGET()
		s.mu.Lock()
		d.waiting = true
		head := &downloadDemand{key: "head"}
		earlier := s.earlierWaiterLocked(head, downloadRank{level: priorityQueueHead})
		s.mu.Unlock()
		s.UpdatePrefetchQueue([]int64{1}, false)
		s.mu.Lock()
		obsoleteEarlier := s.earlierWaiterLocked(head, downloadRank{level: priorityQueueHead})
		s.mu.Unlock()
		s.UpdatePrefetchQueue([]int64{1, 2}, false)
		s.mu.Lock()
		cancel()
		canceledEarlier := s.earlierWaiterLocked(head, downloadRank{level: priorityQueueHead})
		s.mu.Unlock()
		if !earlier {
			t.Fatal("recent-playback waiter lost priority to queue head")
		}
		if obsoleteEarlier {
			t.Fatal("grace resurrected a waiter removed from the queue")
		}
		if canceledEarlier {
			t.Fatal("grace resurrected a canceled queue waiter")
		}
		synctest.Wait()
	})
}

func TestPlaybackGraceShutdownClearsTimersAndDoesNotDelayClose(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payload[:4]))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	assertResponse(t, request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=0-2"}), 206, payload[:3])
	checkResourceLevel(t, s, videoKey(payload), 1)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.playbackGrace) != 0 {
		t.Fatal("shutdown left playback grace timers")
	}
	assertNoPartial(t, cfg.tempDir())
}

// A real blocked prefetch must be woken by expiry, without rank polling or
// another request. Keep the old flight alive until the waiting head preempts it.
func TestPlaybackGraceExpiryWakesWaitingQueueHead(t *testing.T) {
	started := make(chan struct{}, 1)
	var once sync.Once
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("e") == videoKey(payload) {
			w.Write([]byte(payload[:4]))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		once.Do(func() { started <- struct{}{} })
		w.Write([]byte("new"))
	})
	s.cfg.MaxDownloads = 1
	s.slots = make(chan struct{}, 1)
	assertResponse(t, request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=0-2"}), 206, payload[:3])
	queue := context.WithValue(context.Background(), queueDemandKey{}, true)
	s.UpdatePrefetchQueue([]int64{1}, false)
	done := make(chan error, 1)
	go func() { _, err := s.PrefetchSong(queue, "1", videoURL("new")); done <- err }()
	select {
	case <-started:
		t.Fatal("queue head started during playback grace")
	case <-time.After(100 * time.Millisecond):
	}
	receiveDemandEvent(t, started)
	if err := receiveDemandEvent(t, done); err != nil {
		t.Fatal(err)
	}
}
