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

	"still-wanna-dance/internal/vrclog"
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
		s.cfg.Logger = slog.Default()
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
		end := base.Add(90 * time.Second)
		setFormalPlaybackEnd(t, s, end)
		s.UpdatePrefetchQueue([]int64{2, 3}, false)
		at := time.Now()
		s.mu.Lock()
		start := s.queueClockLocked(s.prefetchSources["2"], at).at
		s.mu.Unlock()
		if start != end {
			t.Fatal("departure before the expected end did not immediately update the schedule", start)
		}
		timeline := vrclog.PlayerTimeline{Anchor: vrclog.PlaybackAnchor{SongID: 9, At: at}}
		s.ObservePlaybackLog(&timeline, []vrclog.Event{{Playback: &vrclog.PlaybackEvent{At: at, Kind: "end", SongID: 9}}}, at)
		s.mu.Lock()
		held := s.queueSchedule == queueFixed && s.prefetchSources["2"].startAt == start
		s.mu.Unlock()
		if !held {
			t.Fatal("natural end did not hold the latest published schedule")
		}
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

func TestQueueHeadDepartureRetainsSuccessorSchedule(t *testing.T) {
	for _, tc := range []struct {
		name         string
		endFirst     bool
		timeToOldEnd time.Duration
	}{
		{name: "end log first", endFirst: true, timeToOldEnd: 10 * time.Second},
		{name: "forecast reached before departure", timeToOldEnd: time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w, s := testSession(t)
				s.cfg.Logger = slog.Default()
				now := time.Now()
				end := now.Add(tc.timeToOldEnd)
				setFormalPlaybackEnd(t, s, end)
				s.UpdatePrefetchQueue([]int64{1, 2, 3, 4}, false)
				registerQueueResource(t, s, "1", "head")
				registerQueueResource(t, s, "2", w.v.key)
				registerQueueResource(t, s, "3", "removed")
				timeline := vrclog.PlayerTimeline{Anchor: vrclog.PlaybackAnchor{SongID: 9, At: now}}
				endLog := []vrclog.Event{{Playback: &vrclog.PlaybackEvent{At: now, Kind: "room-end"}}}
				if tc.endFirst {
					s.ObservePlaybackLog(&timeline, endLog, now)
					time.Sleep(time.Second)
					selectedAt := time.Now()
					time.Sleep(time.Second)
					s.ObservePlaybackLog(&timeline, []vrclog.Event{
						{Playback: &vrclog.PlaybackEvent{At: selectedAt, Kind: "room-selection", SongID: 1}},
						{Playback: &vrclog.PlaybackEvent{At: time.Now(), Kind: "room-stop"}},
					}, time.Now())
				}
				time.Sleep(4 * time.Second)
				departed := time.Now()
				s.UpdatePrefetchQueue([]int64{2, 3, 4}, false)
				if !tc.endFirst {
					endLog[0].Playback.At = departed
					s.ObservePlaybackLog(&timeline, endLog, departed)
				}
				second := end.Add(typicalDuration)
				plan, _, _ := w.plan(time.Now())
				if n := plan[0].need; !n.ReadyBy.Equal(second) || !n.Rolling.Base.IsZero() {
					t.Fatalf("successor changed deadline on departure: %+v, want %s", n, second)
				}
				wantHandoff := maxTime(departed, end)
				if tc.endFirst {
					wantHandoff = end
				}
				h := snapshotUseHandoffs(s)
				if len(h) != 1 || h[0].span.key != "head" || !h[0].clock.at.Equal(wantHandoff) || !h[0].until.Equal(departed.Add(handoffGrace)) {
					t.Fatalf("head did not hand off on its prior clock: %+v", h)
				}

				// Delete a middle member and append through an unknown slot.
				time.Sleep(time.Second)
				s.UpdatePrefetchQueue([]int64{2, 4, -1, 5}, false)
				s.UpdatePrefetchQueue([]int64{2, 4, -1, 5}, false)
				s.mu.Lock()
				two, four, five := s.prefetchSources["2"], s.prefetchSources["4"], s.prefetchSources["5"]
				stable := two.position == 0 && two.startAt.Equal(second) &&
					four.position == 1 && four.startAt.Equal(end.Add(3*typicalDuration)) &&
					five.startAt.Equal(end.Add(5*typicalDuration)) &&
					s.prefetchSources["1"] == nil && s.prefetchSources["3"] == nil &&
					s.useBlocks[useBlockID{"removed", 0}] == 0
				s.mu.Unlock()
				if !stable {
					t.Fatal("queue edits failed to preserve surviving slots or withdraw removed members")
				}
				// Another head leaves before a valid clock. Its successor keeps
				// the same exact clock even though the positions change again.
				s.UpdatePrefetchQueue([]int64{4, -1, 5}, false)
				if got := s.queueClockLocked(four, time.Now()).at; !got.Equal(end.Add(3 * typicalDuration)) {
					t.Fatal("successive departure moved the successor", got)
				}
				nextEnd := time.Now().Add(20 * time.Second)
				setFormalPlaybackEnd(t, s, nextEnd)
				s.mu.Lock()
				resumed := s.queueClockLocked(four, time.Now()).at.Equal(nextEnd) &&
					s.queueClockLocked(five, time.Now()).at.Equal(nextEnd.Add(2*typicalDuration))
				s.mu.Unlock()
				if !resumed {
					t.Fatal("valid clock did not replan from the latest queue")
				}
			})
		})
	}
}

func TestHeldQueueExpiresAndRestoresThroughCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		s.cfg.Logger = slog.Default()
		end := time.Now().Add(time.Second)
		setFormalPlaybackEnd(t, s, end)
		s.UpdatePrefetchQueue([]int64{1, 2}, false)
		add := func() {
			t.Helper()
			_, release := addTestDemand(t, s, s.queueDemandContext(t.Context(), "2"),
				video{key: w.v.key, songID: "2", size: 2 * rangeBlockSize}, false)
			release()
		}
		add()
		s.mu.Lock()
		s.prefetchSources["1"].timing = mediaTiming{rangeBlockSize, time.Second}
		s.prefetchSources["2"].timing = mediaTiming{2 * rangeBlockSize, 2 * time.Minute}
		s.retimeQueueLocked(time.Now())
		s.mu.Unlock()
		time.Sleep(time.Second)
		s.UpdatePrefetchQueue([]int64{2}, false)
		firstExpiry := end.Add(time.Second + sourceGrace)
		time.Sleep(time.Until(firstExpiry) - time.Nanosecond)
		synctest.Wait()
		if p := protectionFor(s, w.v.key, 0); !p.active(time.Now()) {
			t.Fatal("first block lost protection before its fixed deadline")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		s.UpdatePrefetchQueue([]int64{2}, false)
		add() // Late retries/metadata refresh cannot re-register an expired prefix.
		plan, _, _ := w.plan(time.Now())
		s.mu.Lock()
		count := len(s.useBlocks)
		s.mu.Unlock()
		if _, found := plan[0]; found || count != 1 || !plan[1].need.ReadyBy.Equal(end.Add(61*time.Second)) {
			t.Fatalf("deadline failed to release only the expired block: plan=%+v capacity=%d", plan, count)
		}
		capacity := useSpan{"capacity", 0, backgroundUseBlocks - 1}
		s.mu.Lock()
		err := s.reserveUseLocked(capacity, false)
		s.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		// The operator can stop the room during the very same log second
		// as natural end. A button event distinguishes that callback.
		at := time.Now().Truncate(time.Second)
		timeline := vrclog.PlayerTimeline{Anchor: vrclog.PlaybackAnchor{SongID: 9, At: at}}
		button, ok, err := vrclog.ParseWithPlayback(at.In(time.Local).Format("2006.01.02 15:04:05") +
			" Debug - [VideoQueueManager] StopVideo Button Clicked")
		if !ok || err != nil {
			t.Fatal("stop button marker was not parsed", err)
		}
		s.ObservePlaybackLog(&timeline, []vrclog.Event{
			{Playback: &vrclog.PlaybackEvent{At: at, Kind: "room-end"}},
			button,
			{Playback: &vrclog.PlaybackEvent{At: at, Kind: "room-stop"}},
		}, time.Now())
		plan, _, _ = w.plan(at)
		if _, found := plan[0]; found || plan[1].need.Rolling.Base.IsZero() {
			t.Fatal("released prefix exceeded capacity or stop kept fixed deadlines")
		}
		s.mu.Lock()
		s.releaseUseLocked(capacity, at)
		s.notifyUseChangedLocked(capacity.key)
		s.mu.Unlock()
		plan, _, _ = w.plan(at)
		if n := plan[0].need; !n.ReadyAt(at).Equal(at) || n.Rolling.Base.IsZero() {
			t.Fatal("stopped clock did not restore the queued block when capacity returned", n)
		}
	})
}
