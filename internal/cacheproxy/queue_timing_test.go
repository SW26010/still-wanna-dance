package cacheproxy

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestQueueUsesEarliestGETEndAndKeepsLastAnchor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		base := time.Now()
		add := func(seconds time.Duration, offset int64) func() {
			p := &playbackClock{at: time.Now(), offset: offset, media: mediaTiming{100, seconds}}
			ctx := context.WithValue(context.Background(), playbackClockKey{}, p)
			_, release := s.addDemand(ctx, w.v, true)
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
		_, release := s.addDemand(context.Background(), w.v, false)
		defer release()
		time.Sleep(time.Minute)
		s.UpdatePrefetchQueue([]int64{1}, false)
		if s.prefetchSources["1"].startAt != time.Now() {
			t.Fatal("background demand became playback")
		}
	})
}
