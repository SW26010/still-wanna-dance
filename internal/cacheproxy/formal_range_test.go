package cacheproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/vrclog"
)

// The origin withholds the future block until the only GET and its 2s
// handoff are both gone. Completing it then requires a real playback source.
func TestFormalPlaybackBridgesRetryAndRejectsBeforeDemand(t *testing.T) {
	body := strings.Repeat("a", int(4*rangeBlockSize))
	target := videoURL(body)
	started, send := make(chan struct{}), make(chan struct{})
	signal := sync.OnceFunc(func() { close(started) })
	release := sync.OnceFunc(func() { close(send) })
	defer release()
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		signal()
		select {
		case <-send:
			serveFixtureContent(w, r, body)
		case <-r.Context().Done():
		}
	}, func(c *Config) {
		c.ResolvePlayback = func(context.Context, string) (string, error) { return target, nil }
	})
	r := httptest.NewRequest("GET", target, nil)
	v, err := s.parseResolved(r)
	if err != nil {
		t.Fatal(err)
	}
	formalCatalog(t, s, v)
	local, err := s.newLocalResource(v, s.cfg.Logger)
	if err != nil {
		t.Fatal(err)
	}
	defer local.storage.release()
	r.Header.Set("User-Agent", "NSPlayer/12")
	r.Header.Set("Range", fmt.Sprintf("bytes=%d-", 3*rangeBlockSize))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(httptest.NewRecorder(), r.WithContext(ctx)) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("GET did not reach origin")
	}
	at := time.Now()
	s.applyPlaybackTimeline(vrclog.PlayerTimeline{Role: vrclog.Follower, Anchor: vrclog.PlaybackAnchor{SongID: 1, At: at, Position: 55}}, true)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("GET did not exit")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		finished := len(s.demands) == 0 && len(s.useHandoffs) == 0
		refs := s.useBlocks[useBlockID{v.key, 3}]
		s.mu.Unlock()
		if finished {
			if refs != 1 {
				t.Fatalf("gap has %d references, want only formal", refs)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("handoff did not expire")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Block 1 is late and missing while the formal source still holds block 3.
	w := request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=1", map[string]string{"User-Agent": "NSPlayer/12", "Range": fmt.Sprintf("bytes=%d-", rangeBlockSize)})
	if w.Code != 504 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Header())
	}
	s.mu.Lock()
	clean := len(s.demands) == 0 && len(s.useHandoffs) == 0 && len(s.useBlocks) == 1
	s.mu.Unlock()
	if !clean {
		t.Fatal("rejected GET registered work")
	}
	release()
	deadline = time.Now().Add(5 * time.Second)
	for !local.has(3) {
		if time.Now().After(deadline) {
			t.Fatal("future block failed to finish without a GET")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFormalLateRangeHTTPPreconditionsAndSongIdentity(t *testing.T) {
	body := strings.Repeat("a", int(2*rangeBlockSize))
	target := videoURL(body)
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		serveFixtureContent(w, r, body)
	}, func(c *Config) {
		c.ResolvePlayback = func(context.Context, string) (string, error) { return target, nil }
	})
	v, err := s.parseResolved(httptest.NewRequest("GET", target, nil))
	if err != nil {
		t.Fatal(err)
	}
	formalCatalog(t, s, v)
	local, err := s.newLocalResource(v, s.cfg.Logger)
	if err != nil {
		t.Fatal(err)
	}
	defer local.storage.release()
	if err := local.storage.store(0, []byte(body[:rangeBlockSize])); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	s.applyPlaybackTimeline(vrclog.PlayerTimeline{Role: vrclog.Follower, Anchor: vrclog.PlaybackAnchor{SongID: 1, At: at, Position: 55}}, true)
	etag := fmt.Sprintf("%q", v.key)
	for _, tc := range []struct {
		field, value string
		status       int
	}{
		{"If-Match", `"other", ` + etag, 504},
		{"If-Match", "W/" + etag, 412},
		{"If-None-Match", `"other"`, 504},
		{"If-None-Match", "W/" + etag, 304},
		{"If-Modified-Since", at.UTC().Format(http.TimeFormat), 504},
		{"If-Unmodified-Since", at.UTC().Format(http.TimeFormat), 504},
	} {
		w := request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=1", map[string]string{
			"User-Agent": "NSPlayer/12", "Range": fmt.Sprintf("bytes=%d-", rangeBlockSize-8192), tc.field: tc.value,
		})
		if w.Code != tc.status {
			t.Fatalf("%s %s: got %d, want %d", tc.field, tc.value, w.Code, tc.status)
		}
		if w.Code == 504 && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cacheable rejection", w.Header())
		}
		if w.Code != 504 && (w.Body.Len() != 0 || w.Header().Get("ETag") != etag) {
			t.Fatal("invalid conditional response", w.Header(), w.Body)
		}
	}
	s.mu.Lock()
	registered := len(s.demands) != 0 || len(s.useHandoffs) != 0 || len(s.useBlocks) != 0
	s.mu.Unlock()
	if registered {
		t.Fatal("conditional response registered block demand")
	}
	// A direct URL for these same bytes could also belong to another song.
	if _, err := s.Store.db.Exec("INSERT INTO songs(id,end) VALUES(2,200)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.db.Exec("INSERT INTO song_media(song_id,md5) VALUES(2,?)", v.key); err != nil {
		t.Fatal(err)
	}
	if local.has(1) {
		t.Fatal("fixture lost its late missing block")
	}
	direct := request(s, "GET", target, map[string]string{"User-Agent": "NSPlayer/12", "Range": fmt.Sprintf("bytes=%d-%d", rangeBlockSize, rangeBlockSize)})
	if direct.Code != http.StatusPartialContent || direct.Body.String() != "a" {
		t.Fatal("resource URL for a shared video", direct.Code, direct.Body)
	}
}

func TestFormalRangeBoundaries(t *testing.T) {
	const size = int64(1 << 20)
	now := time.Now()
	v := video{key: "a", songID: "1", size: 4 * size}
	p := &formalPlayback{song: "1", key: "a", clock: playbackClock{at: now.Add(-time.Second), media: mediaTiming{4 * size, 4 * time.Second}}}
	local := &localResource{video: v, done: map[int64]bool{0: true}}
	s := &Server{cfg: Config{BlockSize: size}, formal: p, localResources: map[string]*localResource{"a": local}, playerRole: vrclog.Follower}
	for _, tc := range []struct {
		start, end int64
		delay      time.Duration
		reject     bool
	}{
		{size, size + 1, 0, false}, {size, size + 1, 1, true}, {size - 8192, size + 1, 1, true},
		{size - 8193, 3 * size, 1, false}, {size - 8192, size, 1, false}, {0, 4 * size, time.Second, false},
		{2 * size, 4 * size, 0, false}, {size, size + 1, 3 * time.Second, false},
	} {
		_, _, got := s.latePlayerBlockLocked(v, byteInterval{tc.start, tc.end}, now.Add(tc.delay))
		if got != tc.reject {
			t.Fatal(tc, got)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Range", "bytes=0-")
	r.Header.Set("If-Range", `"other"`)
	r = r.WithContext(context.WithValue(r.Context(), playbackClockKey{}, &playbackClock{offset: size, limit: 4 * size}))
	if s.playerAdmission(r, v).reject {
		t.Fatal("If-Range selected a full response")
	}
	v.songID = "2"
	if _, _, reject := s.latePlayerBlockLocked(v, byteInterval{size, 2 * size}, now.Add(1)); reject {
		t.Fatal("another song sharing the same media")
	}
	v.songID = ""
	if _, _, reject := s.latePlayerBlockLocked(v, byteInterval{size, 2 * size}, now.Add(1)); reject {
		t.Fatal("resource URL without song identity")
	}
	v.songID = "1"
	v.size++
	if _, _, reject := s.latePlayerBlockLocked(v, byteInterval{size, 2 * size}, now.Add(1)); reject {
		t.Fatal("different media size")
	}
	v.size--
	delete(s.localResources, "a")
	if _, _, reject := s.latePlayerBlockLocked(v, byteInterval{size, 2 * size}, now.Add(1)); reject {
		t.Fatal("unknown availability")
	}
}

func TestFormalStartKeepsFutureBlocksWithoutStartingDownload(t *testing.T) {
	body := strings.Repeat("v", int(3*rangeBlockSize))
	target := videoURL(body)
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		serveFixtureContent(w, r, body)
	}, func(c *Config) {
		c.ResolvePlayback = func(context.Context, string) (string, error) {
			t.Error("room log triggered address resolution")
			return "", fmt.Errorf("unexpected address lookup")
		}
	})
	v, err := s.parseResolved(httptest.NewRequest("GET", target, nil))
	if err != nil {
		t.Fatal(err)
	}
	formalCatalog(t, s, v)
	s.applyPlaybackTimeline(vrclog.PlayerTimeline{Role: vrclog.Owner, Anchor: vrclog.PlaybackAnchor{SongID: 1, At: time.Now(), Position: 55}}, true)
	s.mu.Lock()
	refs := s.useBlocks[useBlockID{v.key, 2}]
	started := s.resourceUses[v.key] != nil || s.flights[v.key] != nil
	s.mu.Unlock()
	if refs != 1 || started {
		t.Fatalf("after log start: future references=%d, download started=%t", refs, started)
	}
	w := request(s, "GET", target, map[string]string{"User-Agent": "NSPlayer/12", "Range": fmt.Sprintf("bytes=%d-", len(body)-1)})
	if w.Code != http.StatusPartialContent || w.Body.String() != "v" {
		t.Fatal("owner GET with a resolved address", w.Code, w.Body)
	}
}

func TestFormalExpiryWithoutTrafficAndReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Server{cfg: Config{BlockSize: 10}}
		start := time.Now()
		play := func(key string, at time.Time) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.clearFormalLocked(time.Now())
			s.updateFormalLocked(&formalPlayback{key: key, song: "1", clock: playbackClock{at: at, media: mediaTiming{30, 3 * time.Second}}}, time.Now(), true)
		}
		check := func(key string, first int64, count int) {
			t.Helper()
			synctest.Wait()
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.useBlocks) != count || len(s.useHandoffs) != 0 {
				t.Fatalf("wanted %d references and no handoffs, got %v", count, s.useBlocks)
			}
			if count == 0 {
				if s.formal != nil {
					t.Fatal("natural end left the playback active")
				}
				return
			}
			if s.formal == nil || s.formal.key != key || s.useBlocks[useBlockID{key, first}] != 1 {
				t.Fatalf("wanted %s/%d as first active block, got %v", key, first, s.useBlocks)
			}
		}
		play("a", start)
		time.Sleep(time.Second)
		check("a", 1, 2)
		time.Sleep(time.Nanosecond)
		check("a", 2, 1)
		play("b", time.Now())
		time.Sleep(2 * time.Second)
		check("b", 2, 1)
		time.Sleep(time.Second)
		check("", 0, 0)
	})
}

func TestFormalWithdrawalKeepsIndependentGETClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Server{cfg: Config{BlockSize: 10}}
		start := time.Now()
		clock := &playbackClock{at: start, offset: 10, limit: 20, media: mediaTiming{30, 30 * time.Second}}
		get, release, err := s.addDemand(context.WithValue(t.Context(), playbackClockKey{}, clock), video{key: "a", size: 30}, true)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		s.mu.Lock()
		s.updateFormalLocked(&formalPlayback{key: "a", clock: playbackClock{at: start, media: mediaTiming{30, 3 * time.Second}}}, start, true)
		s.mu.Unlock()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		s.mu.Lock()
		defer s.mu.Unlock()
		plan, _ := s.collectUsePlanLocked("a", time.Now())
		if s.useBlocks[useBlockID{"a", 1}] != 1 || plan[1].need.ReadyBy != start || get.clock != clock || get.ctx.Err() != nil {
			t.Fatal("formal expiry interfered with independent GET", plan)
		}
	})
}

func TestFormalDelayedExpiryUsesLastReferenceTime(t *testing.T) {
	at := time.Now()
	span := useSpan{"a", 1, 3}
	s := &Server{
		cfg:         Config{BlockSize: 10, LocalProtectionGrace: time.Minute},
		formal:      &formalPlayback{key: "a", use: span, clock: playbackClock{at: at, media: mediaTiming{30, 6 * time.Second}}},
		useHandoffs: useHandoffHeap{&useHandoff{span: span, until: at.Add(3 * time.Second)}},
		useBlocks:   map[useBlockID]int{{"a", 1}: 2, {"a", 2}: 2},
		protection:  map[string]map[int64]blockProtection{"a": {1: {start: at}, 2: {start: at}}},
	}
	// Block 1 expires before the shared handoff, block 2 after it.
	s.expireUseLocked(at.Add(5 * time.Second))
	for block, last := range map[int64]time.Duration{1: 3 * time.Second, 2: 4*time.Second + time.Nanosecond} {
		got := s.protection["a"][block].until
		if want := at.Add(last + time.Minute); !got.Equal(want) {
			t.Errorf("block %d protection ended at %v, want %v", block, got, want)
		}
	}
}

func TestFormalClockWithoutBlocksExpiresAtCorrectedEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Server{cfg: Config{BlockSize: 10}}
		if err := s.reserveUseLocked(useSpan{"occupant", 0, backgroundUseBlocks}, false); err != nil {
			t.Fatal(err)
		}
		at := time.Now()
		p := &formalPlayback{key: "a", clock: playbackClock{at: at, media: mediaTiming{100, 20 * time.Second}}}
		s.mu.Lock()
		s.updateFormalLocked(p, at, true)
		p.setPosition(at, 15)
		s.updateFormalLocked(p, at, true)
		s.mu.Unlock()
		time.Sleep(5*time.Second - time.Nanosecond)
		synctest.Wait()
		s.mu.Lock()
		waiting := s.formal == p && p.use.first == p.use.last
		s.mu.Unlock()
		if !waiting {
			t.Fatal("pending playback ended before its corrected end")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.formal != nil {
			t.Fatal("pending playback outlived its corrected end")
		}
	})
}
