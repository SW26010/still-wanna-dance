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

func setFormalPlaybackEnd(t *testing.T, s *Server, end time.Time) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateFormalLocked(&formalPlayback{
		key: "formal-clock", clock: playbackClock{at: end.Add(-time.Second), media: mediaTiming{1, time.Second}},
	}, time.Now(), false)
}

func TestIdleQueueClockMovesWithoutSourceUpdatesOrDownloads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		s.UpdatePrefetchQueue([]int64{1, 2}, false)
		registerQueueResource(t, s, "1", w.v.key)
		q := s.prefetchSources["1"]
		base := time.Now()
		before, changed, _ := w.plan(base)
		preparationChanged := s.QueuePreparationChanged()
		for _, delay := range []time.Duration{3 * time.Second, 5 * time.Second, 65 * time.Second} {
			time.Sleep(delay)
			synctest.Wait()
			now := time.Now()
			plan, _, _ := w.plan(now)
			s.mu.Lock()
			second := s.queueClockLocked(s.prefetchSources["2"], now).at
			valid := q == s.prefetchSources["1"] && s.useTimer == nil
			s.mu.Unlock()
			if !valid || plan[0].need.Rolling != before[0].need.Rolling ||
				plan[0].need.ReadyAt(now) != now || second != now.Add(typicalDuration) {
				t.Fatal("idle queue time did not advance as a shared, stable clock")
			}
		}
		select {
		case <-changed:
			t.Fatal("wall clock alone rebuilt queue demand")
		default:
		}
		select {
		case <-preparationChanged:
			t.Fatal("ordinary clock renewal woke queue preparation")
		default:
		}
		if p := protectionFor(s, w.v.key, 0); p.start != base.Add(-s.cfg.prepareAhead()) || !p.until.IsZero() {
			t.Fatal("continuous use moved protection entry or created a grace tail", p)
		}
		// Removal freezes handoff timing, even while the next head keeps rolling.
		removed := time.Now()
		s.UpdatePrefetchQueue([]int64{2}, false)
		handoffs := snapshotUseHandoffs(s)
		if q.startAt != removed || len(handoffs) != 1 || handoffs[0].clock.at != removed || handoffs[0].until != removed.Add(handoffGrace) {
			t.Fatal("departure did not freeze the rolling clock at the exit time", handoffs)
		}
		time.Sleep(25 * time.Second)
		synctest.Wait()
		s.mu.Lock()
		valid := q.startAt == removed && len(s.useHandoffs) == 0 && len(s.useBlocks) == 0
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

func TestQueueFollowsFormalEndAcrossGETAndNaturalExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, s := testSession(t)
		base := time.Now()
		check := func(want time.Time) {
			t.Helper()
			s.mu.Lock()
			defer s.mu.Unlock()
			if got := s.queueClockLocked(s.prefetchSources["1"], time.Now()).at; got != want {
				t.Fatalf("queue start=%v want=%v", got, want)
			}
		}
		setFormalPlaybackEnd(t, s, base.Add(time.Minute))
		s.UpdatePrefetchQueue([]int64{1}, false)
		check(base.Add(time.Minute))
		clock := &playbackClock{at: base, limit: 1, media: mediaTiming{1, 3 * time.Minute}}
		ctx := context.WithValue(t.Context(), playbackClockKey{}, clock)
		_, release := addTestDemand(t, s, ctx, video{key: "get", size: 1}, true)
		defer release()
		time.Sleep(50 * time.Second)
		check(base.Add(time.Minute))
		time.Sleep(25 * time.Second)
		synctest.Wait()
		check(time.Now())
		release()
		check(time.Now())
	})
}

func TestQueueDownloadSurvivesFormerDeadline(t *testing.T) {
	for _, status := range []int{http.StatusPartialContent} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := fixtureConfig()
				prepareFixtureResources(t, &cfg, t.TempDir(), true)
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
				now := time.Now()
				clock := &playbackClock{at: now.Add(-time.Second), limit: 1, media: mediaTiming{1, time.Second}}
				getCtx := context.WithValue(t.Context(), playbackClockKey{}, clock)
				_, release := addTestDemand(t, s, getCtx, video{key: "stale-get", size: 1}, true)
				defer release()
				setFormalPlaybackEnd(t, s, now.Add(10*time.Second))
				s.UpdatePrefetchQueue([]int64{1}, false)
				ctx := s.queueDemandContext(t.Context(), "1")
				if _, err := s.PrefetchSong(ctx, "1", videoURL(payload)); err != nil {
					t.Fatal("queue download expired instead of continuing", err)
				}
				if calls != 1 {
					t.Fatal("clock refresh restarted the download", calls)
				}
			})
		})
	}
}

func TestQueueRecalculatesAfterFormalClockCorrectionAndStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		base := time.Now()
		setFormalPlaybackEnd(t, s, base.Add(300*time.Second))
		s.UpdatePrefetchQueue([]int64{1, 2, 3}, false)
		registerQueueResource(t, s, "1", w.v.key)
		check := func(want time.Time) {
			t.Helper()
			s.mu.Lock()
			defer s.mu.Unlock()
			for i, id := range []string{"1", "2", "3"} {
				if got := s.queueClockLocked(s.prefetchSources[id], time.Now()).at; got != want.Add(time.Duration(i)*typicalDuration) {
					t.Fatal(id, got, want)
				}
			}
		}
		check(base.Add(300 * time.Second))
		time.Sleep(10 * time.Second)
		setFormalPlaybackEnd(t, s, base.Add(80*time.Second))
		check(base.Add(80 * time.Second))
		s.mu.Lock()
		s.clearFormalLocked(time.Now())
		s.mu.Unlock()
		check(time.Now())
		setFormalPlaybackEnd(t, s, base.Add(90*time.Second))
		s.UpdatePrefetchQueue([]int64{2, 3}, false)
		setFormalPlaybackEnd(t, s, base.Add(100*time.Second))
		handoffs := snapshotUseHandoffs(s)
		if len(handoffs) != 1 || handoffs[0].clock.at != base.Add(90*time.Second) {
			t.Fatal("removed source's deadline changed")
		}
	})
}

func TestQueueUsesCatalogDurationAndRefreshesUnknownSize(t *testing.T) {
	s, _ := setup(t, nil)
	setFormalPlaybackEnd(t, s, time.Now().Add(time.Hour))
	ctx := context.Background()
	key := "0123456789abcdef0123456789abcdef"
	if _, err := s.Store.usage.db.Exec(`INSERT INTO media(md5) VALUES (?)`, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.usage.db.Exec(`INSERT INTO songs(id,start,end) VALUES (1,50,100),(2,5,300),(3,0,NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.usage.db.Exec("UPDATE songs SET name='本地清单名称' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.usage.db.Exec(`INSERT INTO song_media(song_id,md5) VALUES (1,?),(2,?),(3,?)`, key, key, key); err != nil {
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
	before := q.readyBy(1, rangeBlockSize)
	queueCtx := s.queueDemandContext(ctx, "1")
	if _, err := s.Store.usage.db.Exec("UPDATE media SET byte_size=50000000 WHERE md5=?", key); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshQueueResourceTiming(queueCtx, video{key: key, songID: "1"}); err != nil {
		t.Fatal(err)
	}
	if q.startAt != base || !q.readyBy(1, rangeBlockSize).Before(before) || q.timing != (mediaTiming{50_000_000, 100 * time.Second}) {
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
