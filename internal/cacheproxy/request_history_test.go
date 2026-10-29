package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHistoryPagesAllRequestsAndPreservesIdentity(t *testing.T) {
	s, cfg := setup(t, nil)
	key := strings.Repeat("a", 32)
	var events []usageEvent
	for i := 0; i < 1201; i++ {
		events = append(events, usageEvent{at: 1000 + int64(i/3), completedAt: 2000, key: key, songID: "42", method: "GET", outcome: "completed"})
	}
	events = append(events, usageEvent{at: 1200, method: "HEAD"})
	if err := s.usage.write(events); err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec(`INSERT INTO songs(id,name) VALUES(42,'new mapping'),(43,'new association'); INSERT INTO media(md5) VALUES(?),(?)`, key, strings.Repeat("b", 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec(`INSERT INTO song_media VALUES(42,?),(43,?)`, strings.Repeat("b", 32), key); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	q := RequestQuery{Limit: 50}
	seen := map[int64]bool{}
	for {
		page, err := ReadRequestHistory(context.Background(), cfg.Store.path, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Requests) > 50 {
			t.Fatal("unbounded page")
		}
		for _, r := range page.Requests {
			if seen[r.ID] || r.SongID != "42" || r.Resource != key || r.CompletedAt == nil || *r.CompletedAt != 2000 {
				t.Fatalf("changed/duplicated history: %+v", r)
			}
			seen[r.ID] = true
		}
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" || page.NextCursor == q.Cursor {
			t.Fatal("cursor did not advance")
		}
		q.Cursor = page.NextCursor
	}
	if len(seen) != 1201 {
		t.Fatal("history truncated", len(seen))
	}
	page, err := ReadRequestHistory(context.Background(), cfg.Store.path, RequestQuery{From: 1100, To: 1101, Limit: 50})
	if err != nil || len(page.Requests) != 3 {
		t.Fatal("time boundaries", page, err)
	}
}

func TestLatencyRollingQuantilesCoverageAndRestart(t *testing.T) {
	s, cfg := setup(t, nil)
	now := time.Now().Truncate(time.Second)
	start := now.Add(-80 * time.Hour).UnixMilli()
	if _, err := s.usage.db.Exec("UPDATE request_observation SET started_at=?,retained_since=0", start); err != nil {
		t.Fatal(err)
	}
	var events []usageEvent
	for _, blocking := range []bool{false, true} {
		for i := 1; i <= 20; i++ {
			e := usageEvent{method: "GET", status: 206, outcome: "completed", at: now.Add(-time.Hour).UnixMilli(), bytes: 1, firstBodyNS: int64(i) * int64(time.Millisecond), firstBodyBlocking: &blocking}
			if blocking {
				e.firstBodyNS *= 10
			}
			if i%2 == 0 {
				e.outcome = "canceled"
			}
			events = append(events, e)
		}
	}
	ready := false
	for _, at := range []int64{now.Add(-72*time.Hour).UnixMilli() - 1, now.UnixMilli()} {
		events = append(events, usageEvent{method: "GET", status: 200, at: at, bytes: 1, firstBodyNS: 1, firstBodyBlocking: &ready})
	}
	// Supported records with unknown timing, non-video errors and empty bodies
	// remain history but cannot turn into zero-millisecond latency samples.
	for _, e := range []usageEvent{{status: 200, bytes: 1}, {status: 502, bytes: 5, firstBodyBlocking: &ready}, {status: 200, firstBodyBlocking: &ready}} {
		e.at = now.Add(-time.Hour).UnixMilli()
		e.method = "GET"
		events = append(events, e)
	}
	if err := s.usage.write(events); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := ReadFirstBodyLatency(context.Background(), cfg.Store.path, now)
	if err != nil || r.Partial || r.Ready.Samples != 20 || r.Blocking.Samples != 20 || r.Ready.P50MS == nil || *r.Ready.P50MS != 10 || *r.Ready.P95MS != 19 || *r.Blocking.P50MS != 100 || *r.Blocking.P95MS != 190 {
		t.Fatalf("quantiles: %+v %v", r, err)
	}
	var openErr error
	reopenFixtureStore(t, &cfg)
	s, openErr = New(cfg)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Stop automatic pruning before changing the fixture retention interval.
	// Its startup timer uses the wall clock; this check uses the fixed 'now'.
	if err := s.usage.close(); err != nil {
		t.Fatal(err)
	}
	s.usage.retention, s.usage.unlimited = 30*time.Minute, false
	if _, err := s.usage.prune(now); err != nil {
		t.Fatal(err)
	}
	r, err = ReadFirstBodyLatency(context.Background(), cfg.Store.path, now)
	if err != nil || !r.Partial || r.AvailableFrom == nil || *r.AvailableFrom != now.Add(-30*time.Minute).UnixMilli() || r.Ready.Samples != 0 || r.Ready.P50MS != nil {
		t.Fatalf("short retention: %+v %v", r, err)
	}
}

func TestHistoryCapturesEffectiveResponseAndVideoBytes(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	s.wg.Wait()
	for _, tc := range []struct {
		header, ifRange string
		want            int64
		status          int
	}{{"bytes=-3", "", 3, 206}, {"bytes=33-100", "", 3, 206}, {"bytes=1-2", `"changed"`, 36, 200}, {"bytes=100-", "", -1, 416}} {
		w := request(s, "GET", videoURL(payload), map[string]string{"Range": tc.header, "If-Range": tc.ifRange})
		if w.Code != tc.status {
			t.Fatal(w.Code)
		}
		s.usage.flush()
		page, err := ReadRequestHistory(context.Background(), cfg.Store.path, RequestQuery{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		r := page.Requests[0]
		if r.SongID != "" {
			t.Fatal("resource path inferred song ID", r.SongID)
		}
		if tc.want < 0 {
			if r.ResponseBytes != nil || r.Bytes != 0 || r.FirstBodyNS != nil {
				t.Fatalf("error body counted: %+v", r)
			}
		} else if r.ResponseBytes == nil || *r.ResponseBytes != tc.want || r.Bytes != tc.want || r.FirstBodyNS == nil || r.FirstBodyBlocking == nil || *r.FirstBodyBlocking {
			t.Fatalf("effective range: %+v", r)
		}
	}
}

func TestResolutionFailureRetainsArrivalIdentityWithoutBodySample(t *testing.T) {
	entered, resume := make(chan struct{}), make(chan struct{})
	s, cfg := setup(t, nil, func(c *Config) {
		c.ResolvePlayback = func(context.Context, string) (string, error) {
			close(entered)
			<-resume
			return "", errors.New("offline")
		}
	})
	done := make(chan struct{})
	before := time.Now().UnixMilli()
	go func() { request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=42", nil); close(done) }()
	<-entered
	time.Sleep(20 * time.Millisecond)
	afterResolution := time.Now().UnixMilli()
	close(resume)
	<-done
	s.usage.flush()
	page, err := ReadRequestHistory(context.Background(), cfg.Store.path, RequestQuery{Limit: 50})
	if err != nil || len(page.Requests) != 1 {
		t.Fatal(page, err)
	}
	r := page.Requests[0]
	if r.SongID != "42" || r.Resource != "" || r.At < before || r.At >= afterResolution || r.CompletedAt == nil || *r.CompletedAt < afterResolution || r.Bytes != 0 || r.ResponseBytes != nil || r.FirstBodyNS != nil || r.Outcome != "failed" {
		t.Fatalf("resolution history: %+v", r)
	}
}

func TestFirstBodyClassOnlyObservesActualPreBodyBlockWait(t *testing.T) {
	for _, scenario := range []string{"ready", "during-registration", "blocking", "after-first"} {
		t.Run(scenario, func(t *testing.T) {
			sp := &spool{blocks: newBlockFiles(t.TempDir(), 2, 1, nil), changed: make(chan struct{}), refs: 1}
			defer sp.release()
			if scenario == "ready" || scenario == "after-first" {
				if _, err := sp.WriteAt([]byte{1}, 0); err != nil {
					t.Fatal(err)
				}
			}
			sp.demand = func(offset int64) func() {
				if scenario == "during-registration" {
					sp.WriteAt([]byte{1}, offset)
				}
				return func() {}
			}
			reader := sp.reader(context.Background(), 2)
			defer reader.Close()
			expected := int64(2)
			response := &responseWriter{ResponseWriter: httptest.NewRecorder(), started: time.Now(), expectedBytes: &expected}
			waited := make(chan struct{}, 2)
			reader.onWait = func() {
				if response.videoBytes == 0 {
					response.waitedForBlock = true
				}
				waited <- struct{}{}
			}
			read := func() {
				b := make([]byte, 1)
				n, err := reader.Read(b)
				if err != nil {
					t.Error(err)
					return
				}
				response.Write(b[:n])
			}
			done := make(chan struct{})
			go func() {
				read()
				if scenario == "after-first" {
					read()
				}
				close(done)
			}()
			if scenario == "blocking" || scenario == "after-first" {
				<-waited
				offset := int64(0)
				if scenario == "after-first" {
					offset = 1
				}
				if _, err := sp.WriteAt([]byte{1}, offset); err != nil {
					t.Fatal(err)
				}
			}
			<-done
			if response.firstBodyBlocking != (scenario == "blocking") {
				t.Fatal("incorrect first-body classification", scenario, response.firstBodyBlocking)
			}
		})
	}
}

func TestHistorySortsWholeRangeWithStableCursorAndUnknownLast(t *testing.T) {
	s, cfg := setup(t, nil)
	keys := []string{strings.Repeat("b", 32), strings.Repeat("a", 32), ""}
	songs := []string{"10", "2", ""}
	var events []usageEvent
	for i := 0; i < 165; i++ {
		events = append(events, usageEvent{at: 1000 + int64(i/6), completedAt: 2000, key: keys[i%3], songID: songs[i%3], method: "GET", outcome: "completed"})
	}
	events = append(events, usageEvent{at: 1010, method: "HEAD"})
	if err := s.usage.write(events); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"time", "song", "resource"} {
		for _, direction := range []string{"ascending", "descending"} {
			t.Run(field+"/"+direction, func(t *testing.T) {
				// IDs 7..162 are in the selected window. Adjacent arrivals tie in pairs
				// within each identity group, and the unknown group spans several pages.
				var want []int64
				groups := []int{-1}
				if field != "time" {
					groups = []int{1, 0, 2}
					if direction == "descending" {
						groups = []int{0, 1, 2}
					}
				}
				for _, group := range groups {
					for n := 0; n < 156; n++ {
						i := 6 + n
						if direction == "descending" {
							i = 161 - n
						}
						if group < 0 || i%3 == group {
							want = append(want, int64(i+1))
						}
					}
				}
				q := RequestQuery{From: 1001, To: 1027, Limit: 17, Sort: field, Direction: direction}
				var got []int64
				for pages := 0; pages < 20; pages++ {
					page, err := ReadRequestHistory(t.Context(), cfg.Store.path, q)
					if err != nil {
						t.Fatal(err)
					}
					for _, r := range page.Requests {
						got = append(got, r.ID)
					}
					if !page.HasMore {
						break
					}
					// A cursor belongs to this ordering, including its direction.
					other := q
					other.Cursor = page.NextCursor
					if direction == "ascending" {
						other.Direction = "descending"
					} else {
						other.Direction = "ascending"
					}
					if other.Validate() == nil {
						t.Fatal("accepted a cursor from another order")
					}
					q.Cursor = page.NextCursor
					if _, err := s.usage.db.Exec("UPDATE request_events SET completed_at=completed_at+1"); err != nil {
						t.Fatal(err)
					}
				}
				if !slices.Equal(got, want) {
					t.Fatalf("global order or page boundary changed:\ngot %v\nwant %v", got, want)
				}
			})
		}
	}
	for _, q := range []RequestQuery{{Limit: 50, Sort: "invalid"}, {Limit: 50, Direction: "sideways"}, {Limit: 50, Cursor: "not-a-cursor"}, {Limit: 50, Sort: "song", Cursor: "1000:1"}} {
		if q.Validate() == nil {
			t.Fatalf("accepted invalid query: %+v", q)
		}
	}
}
