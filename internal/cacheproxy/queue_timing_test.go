package cacheproxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func holdQueuePlaybackUntil(t *testing.T, s *Server, end time.Time) func() {
	t.Helper()
	clock := &playbackClock{at: end.Add(-time.Second), limit: 1, media: mediaTiming{1, time.Second}}
	ctx := context.WithValue(context.Background(), playbackClockKey{}, clock)
	_, release := addTestDemand(t, s, ctx, video{key: "queue-clock", size: 1}, true)
	t.Cleanup(release)
	return release
}

func TestIdleQueueRetimesWithoutUpdatesOrDownloads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		s.UpdatePrefetchQueue([]int64{1, 2}, false)
		registerQueueResource(t, s, "1", w.v.key)
		q := s.prefetchSources["1"]
		base := time.Now()
		_, changed, _ := w.plan(time.Now(), true)
		preparationChanged := s.QueuePreparationChanged()
		time.Sleep(75 * time.Second)
		synctest.Wait()
		want := base.Add(70 * time.Second)
		s.mu.Lock()
		valid := q == s.prefetchSources["1"] && !q.expired && q.readyBy(0) == want && s.prefetchSources["2"].startAt == want.Add(typicalDuration)
		s.mu.Unlock()
		if !valid {
			t.Fatal("idle queue did not keep its source and roll the shared clock")
		}
		select {
		case <-changed:
		default:
			t.Fatal("clock change did not notify downstream consumers")
		}
		select {
		case <-preparationChanged:
			t.Fatal("ordinary clock renewal woke queue preparation")
		default:
		}
		if p := protectionFor(s, w.v.key, 0); p.start != want.Add(-s.protectionAhead()) || !p.until.IsZero() {
			t.Fatal("download and protection clocks diverged or created a grace tail", p)
		}
		// Removal freezes handoff timing, even while the next head keeps rolling.
		s.UpdatePrefetchQueue([]int64{2}, false)
		removed := q.startAt
		time.Sleep(25 * time.Second)
		synctest.Wait()
		s.mu.Lock()
		valid = q.startAt == removed && len(s.useHandoffs) == 0 && len(s.useBlocks) == 0
		s.mu.Unlock()
		if !valid {
			t.Fatal("idle clock renewed a removed source or its fixed handoff")
		}
		for _, ids := range [][]int64{nil, {-1}} {
			s.UpdatePrefetchQueue(ids, true)
			s.mu.Lock()
			stopped := s.useTimer == nil
			s.mu.Unlock()
			if !stopped {
				t.Fatal("queue without usable members left an idle clock timer", ids)
			}
		}
	})
}

func TestIdleQueuePreservesFutureGETEstimateAndLiveGETClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, s := testSession(t)
		base := time.Now()
		check := func(want time.Time, stopped bool) {
			t.Helper()
			s.mu.Lock()
			defer s.mu.Unlock()
			if got := s.prefetchSources["1"].startAt; got != want || (s.useTimer == nil) != stopped {
				t.Fatalf("queue start=%v want=%v, timer stopped=%v want=%v", got, want, s.useTimer == nil, stopped)
			}
		}
		release := holdQueuePlaybackUntil(t, s, base.Add(time.Minute))
		s.UpdatePrefetchQueue([]int64{1}, false)
		release() // Transmission ended; buffered playback can still be running.
		time.Sleep(50 * time.Second)
		synctest.Wait()
		check(base.Add(time.Minute), false)
		time.Sleep(25 * time.Second)
		synctest.Wait()
		check(base.Add(70*time.Second), false)
		// A live GET remains authoritative even when its estimated end is past.
		holdQueuePlaybackUntil(t, s, base.Add(time.Minute))
		time.Sleep(time.Minute)
		synctest.Wait()
		check(base.Add(time.Minute), true)
	})
}

func TestIdleQueueDownloadSurvivesOriginalDeadline(t *testing.T) {
	for _, status := range []int{http.StatusPartialContent, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := fixtureConfig()
				cfg.StorageDir = t.TempDir()
				cfg.Logger = slog.New(slog.NewTextHandler(testLogWriter{t}, nil))
				calls := 0
				cfg.ResourceTransports = func(string) []http.RoundTripper {
					return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						calls++
						first := true
						body := strings.NewReader(payload)
						header := make(http.Header)
						if status == http.StatusPartialContent {
							header.Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(payload)-1, len(payload)))
						}
						return &http.Response{StatusCode: status, Request: r, ContentLength: int64(len(payload)), Header: header,
							Body: io.NopCloser(publishReaderFunc(func(p []byte) (int, error) {
								if first {
									first = false
									select {
									case <-time.After(75 * time.Second):
									case <-r.Context().Done():
										return 0, r.Context().Err()
									}
								}
								return body.Read(p)
							}))}, nil
					})}
				}
				s, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := s.Close(); err != nil {
						t.Error(err)
					}
				})
				s.UpdatePrefetchQueue([]int64{1}, false)
				ctx := s.queueDemandContext(t.Context(), "1")
				if _, err := s.PrefetchSong(ctx, "1", videoURL(payload)); err != nil {
					t.Fatal("idle queue download expired instead of continuing", err)
				}
				if calls != 1 {
					t.Fatal("clock refresh restarted the download", calls)
				}
			})
		})
	}
}

func TestQueueUsesEarliestGETEndAndKeepsLastAnchor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		base := time.Now()
		add := func(seconds time.Duration, offset int64) func() {
			p := &playbackClock{at: time.Now(), offset: offset, limit: 100, media: mediaTiming{100, seconds}}
			ctx := context.WithValue(context.Background(), playbackClockKey{}, p)
			_, release := addTestDemand(t, s, ctx, w.v, true)
			return release
		}
		first := add(300*time.Second, 0)
		defer first()
		time.Sleep(10 * time.Second)
		second := add(200*time.Second, 75)
		defer second()
		s.UpdatePrefetchQueue([]int64{1, 2, 3}, false)
		check := func(want time.Time) {
			t.Helper()
			for i, id := range []string{"1", "2", "3"} {
				if got := s.prefetchSources[id].readyBy(0); got != want.Add(time.Duration(i)*typicalDuration) {
					t.Fatal(id, got, want)
				}
			}
		}
		check(base.Add(60 * time.Second))
		second()
		check(base.Add(300 * time.Second))
		first()
		check(base.Add(300 * time.Second))
		time.Sleep(20 * time.Second)
		s.UpdatePrefetchQueue([]int64{1, 2, 3}, false)
		check(base.Add(300 * time.Second))
		third := add(100*time.Second, 50)
		defer third()
		check(base.Add(80 * time.Second))
		old := s.prefetchSources["1"]
		s.UpdatePrefetchQueue([]int64{2, 3}, false)
		third()
		if old.readyBy(0) != base.Add(80*time.Second) {
			t.Fatal("removed source's deadline changed")
		}
	})
}

func TestQueueUsesCatalogDurationAndRefreshesUnknownSize(t *testing.T) {
	s, _ := setup(t, nil)
	holdQueuePlaybackUntil(t, s, time.Now().Add(time.Hour))
	ctx := context.Background()
	key := "0123456789abcdef0123456789abcdef"
	if _, err := s.usage.db.Exec(`INSERT INTO media(md5) VALUES (?)`, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec(`INSERT INTO songs(song_id,start,end) VALUES (1,50,100),(2,5,300),(3,0,NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec("UPDATE songs SET name='本地清单名称' WHERE song_id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec(`INSERT INTO song_media(song_id,md5) VALUES (1,?),(2,?),(3,?)`, key, key, key); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePrefetchQueue([]int64{1, -1, 2, 3}, false); err != nil {
		t.Fatal(err)
	}
	base := s.prefetchSources["1"].startAt
	titles := s.QueueTitles()
	if titles["1"] != "本地清单名称" || titles["2"] != "" {
		t.Fatal(titles)
	}
	titles["1"] = "caller mutation"
	if s.QueueTitles()["1"] != "本地清单名称" {
		t.Fatal("mutable catalog title snapshot")
	}
	if s.prefetchSources["2"].startAt != base.Add(100*time.Second+typicalDuration) || s.prefetchSources["3"].startAt != base.Add(400*time.Second+typicalDuration) {
		t.Fatal("queue ignored catalog durations or unknown position")
	}
	q := s.prefetchSources["1"]
	before := q.readyBy(1)
	queueCtx := s.queueDemandContext(ctx, "1")
	if _, err := s.usage.db.Exec("UPDATE media SET byte_size=50000000 WHERE md5=?", key); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshQueueResourceTiming(queueCtx, video{key: key, songID: "1"}); err != nil {
		t.Fatal(err)
	}
	if q.startAt != base || !q.readyBy(1).Before(before) || q.timing != (mediaTiming{50_000_000, 100 * time.Second}) {
		t.Fatal("resolved size did not refine deadline", q)
	}
	// A repeated order refreshes newly available metadata for other members.
	if err := s.UpdatePrefetchQueue([]int64{1, -1, 2, 3}, false); err != nil {
		t.Fatal(err)
	}
	if s.prefetchSources["3"].timing.duration != 200*time.Second || q.startAt != base {
		t.Fatal("metadata refresh slid queue base")
	}
	s.UpdatePrefetchQueue([]int64{2, 3}, false)
	if err := s.refreshQueueResourceTiming(queueCtx, video{key: key, songID: "1", size: 1}); err != nil {
		t.Fatal(err)
	}
	if q.startAt != base {
		t.Fatal("late resolution altered removed queue source")
	}
}

func TestBatchCannotSetPlaybackQueueAnchor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		_, release := addTestDemand(t, s, context.Background(), w.v, false)
		defer release()
		time.Sleep(time.Minute)
		s.UpdatePrefetchQueue([]int64{1}, false)
		if s.prefetchSources["1"].startAt != time.Now() {
			t.Fatal("background demand became playback")
		}
	})
}
