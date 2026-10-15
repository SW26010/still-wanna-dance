package cacheproxy

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"testing"
	"testing/synctest"
	"time"
)

func TestHTTPRangeUpdatesQueuePlaybackAnchor(t *testing.T) {
	body := bytes.Repeat([]byte("x"), int(2*rangeBlockSize))
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
	v, _ := s.parseResolved(r)
	if _, err := s.usage.db.Exec("INSERT INTO media(md5,byte_size) VALUES (?,?)", v.key, v.size); err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec("INSERT INTO songs(song_id,end) VALUES (1,120)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec("INSERT INTO song_media(song_id,md5) VALUES (1,?)", v.key); err != nil {
		t.Fatal(err)
	}
	s.UpdatePrefetchQueue([]int64{2}, false)
	before := time.Now()
	w := request(s, "GET", videoURL(string(body)), map[string]string{"Range": "bytes=1048576-1048583"})
	if w.Code != http.StatusPartialContent || w.Body.String() != "xxxxxxxx" {
		t.Fatal(w.Code, w.Body.String())
	}
	after := time.Now()
	s.mu.Lock()
	start := s.prefetchSources["2"].startAt
	s.mu.Unlock()
	if start.Before(before.Add(time.Minute)) || start.After(after.Add(time.Minute)) {
		t.Fatal("GET arrival/Range was not propagated to the queue", before, start, after)
	}
}

func TestHTTPDateRangeUsesActualPlaybackOffset(t *testing.T) {
	body := bytes.Repeat([]byte("x"), int(2*rangeBlockSize))
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	target := videoURL(string(body))
	r, _ := http.NewRequest("GET", target, nil)
	v, _ := s.parseResolved(r)
	if w := request(s, "GET", target, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if _, err := s.usage.db.Exec("INSERT INTO songs(song_id,end) VALUES (1,120)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec("INSERT INTO song_media(song_id,md5) VALUES (1,?)", v.key); err != nil {
		t.Fatal(err)
	}
	date := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(cfg.videoFile(v.key), date, date); err != nil {
		t.Fatal(err)
	}
	s.UpdatePrefetchQueue([]int64{2}, false)
	for _, valid := range []bool{true, false} {
		validator, remaining, status := date, time.Minute, 206
		if !valid {
			validator = validator.Add(-time.Second)
			remaining = 2 * time.Minute
			status = 200
		}
		before := time.Now()
		w := request(s, "GET", target, map[string]string{"Range": "bytes=1048576-1048583", "If-Range": validator.Format(http.TimeFormat)})
		after := time.Now()
		if w.Code != status {
			t.Fatal(valid, w.Code)
		}
		s.mu.Lock()
		start := s.queueStart
		s.mu.Unlock()
		if start.Before(before.Add(remaining)) || start.After(after.Add(remaining)) {
			t.Fatal(valid, start, before, after)
		}
	}
}

func TestPlaybackRangePosition(t *testing.T) {
	v := video{key: "checksum", size: 1000}
	for _, tc := range []struct {
		header, validator string
		offset            int64
		valid             bool
	}{
		{"", "", 0, true}, {"bytes=250-", "", 250, true}, {"bytes=250-700", "", 250, true},
		{"bytes=-100", "", 900, true}, {"bytes=-2000", "", 0, true}, {"bytes=1000-", "", 0, false},
		{"bytes=-0", "", 0, false}, {"bytes=50-10", "", 0, false}, {"bytes=1-2,4-5", "", 0, false},
		{"bytes=250-", `"checksum"`, 250, true}, {"bytes=250-", `"other"`, 0, true},
	} {
		r, _ := http.NewRequest("GET", "http://example.test", nil)
		r.Header.Set("Range", tc.header)
		r.Header.Set("If-Range", tc.validator)
		offset, valid := playbackRangeStart(r, v)
		if offset != tc.offset || valid != tc.valid {
			t.Fatal(tc, offset, valid)
		}
	}
	r, _ := http.NewRequest("HEAD", "http://example.test", nil)
	if _, valid := playbackRangeStart(r, v); valid {
		t.Fatal("HEAD became a playback observation")
	}
}

func TestPlaybackDeadlinesStayAtRequestArrival(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		at := time.Now()
		clock := &playbackClock{at: at, offset: 8 * rangeBlockSize, media: mediaTiming{20 * rangeBlockSize, 200 * time.Second}}
		ctx := context.WithValue(context.Background(), playbackClockKey{}, clock)
		_, release := s.addDemand(ctx, w.v, true)
		defer release()
		advance, closeReader := w.track(ctx)
		defer closeReader()
		time.Sleep(10 * time.Second) // Resolution/download delays do not move the clock.
		advance(12 * rangeBlockSize)
		check := func(block int64, want time.Time) {
			t.Helper()
			plan, _ := w.plan(time.Now(), true)
			if len(plan[block]) == 0 {
				t.Fatal("missing block", block)
			}
			for _, p := range plan[block] {
				if p.need.ReadyBy != want || p.need.Blocking {
					t.Fatal(block, p.need, want)
				}
			}
		}
		check(0, at)
		check(12, at.Add(40*time.Second))
		time.Sleep(20 * time.Second)
		advance(13 * rangeBlockSize)
		check(13, at.Add(50*time.Second))
		if clock.end() != at.Add(120*time.Second) {
			t.Fatal("incorrect remaining playback time", clock.end())
		}
	})
}

func TestPlaybackContextUsesArrivalAndIgnoresInvalidRanges(t *testing.T) {
	s, _ := setup(t, nil)
	v := video{key: "0123456789abcdef0123456789abcdef", size: 60_000_000}
	r, _ := http.NewRequest("GET", "http://example.test", nil)
	r.Header.Set("Range", "bytes=15000000-16000000")
	at := time.Now().Add(-15 * time.Second)
	ctx, err := s.playbackContext(r, v, at)
	if err != nil {
		t.Fatal(err)
	}
	p := clockFromContext(ctx)
	if p == nil || p.at != at || p.offset != 15_000_000 || p.end() != at.Add(180*time.Second) {
		t.Fatal(p)
	}
	r.Header.Set("Range", "bytes=60000000-")
	ctx, err = s.playbackContext(r, v, at)
	if err != nil {
		t.Fatal(err)
	}
	d, release := s.addDemand(ctx, v, true)
	defer release()
	if d.clock != nil {
		t.Fatal("invalid HTTP range gained an implicit clock")
	}
}
