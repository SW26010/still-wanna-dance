package cacheproxy

import (
	"context"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

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
