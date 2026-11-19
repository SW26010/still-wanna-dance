package cacheproxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/vrclog"
)

func formalCatalog(t *testing.T, s *Server, v video) {
	t.Helper()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO media(md5,byte_size) VALUES (?,?)", []any{v.key, v.size}},
		{"INSERT INTO songs(id,end) VALUES (1,100)", nil},
		{"INSERT INTO song_media(song_id,md5) VALUES (1,?)", []any{v.key}},
	} {
		if _, err := s.Store.usage.db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlayerOwnerHeadersBeforeMissingBody(t *testing.T) {
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
			serveFixtureContent(w, r, payload)
		case <-r.Context().Done():
		}
	})
	s.playerRole = vrclog.Owner
	local := httptest.NewServer(s)
	defer local.Close()
	defer unblock()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, "GET", local.URL+strings.TrimPrefix(videoURL(payload), "http://play.udon.dance"), nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Host = "play.udon.dance"
	r.Header.Set("User-Agent", "NSPlayer/12")
	r.Header.Set("Range", "bytes=4-")
	response, err := local.Client().Do(r)
	if err != nil {
		t.Fatal("owner headers before first block", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 206 || response.Header.Get("Content-Range") != "bytes 4-35/36" {
		t.Fatal(response.StatusCode, response.Header)
	}
	unblock()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != payload[4:] {
		t.Fatal(string(body), err)
	}
}

func TestPlayerFollowerHoldsHeadersWhileWaitingForFirstBlock(t *testing.T) {
	v := video{key: videoKey(payload), path: "video.mp4", size: int64(len(payload))}
	sp := &spool{blocks: newBlockFiles(t.TempDir(), v.size, rangeBlockSize, nil), refs: 1, changed: make(chan struct{})}
	defer sp.release()
	s := &Server{playerRole: vrclog.Follower}
	r := httptest.NewRequest("GET", "/video.mp4", nil)
	r.Header.Set("User-Agent", "NSPlayer/12")
	r.Header.Set("Range", "bytes=4-")
	response := httptest.NewRecorder()
	w := &responseWriter{ResponseWriter: response}
	if s.admitPlayerGET(w, r, v, nil, slog.Default()) {
		t.Fatal("unexpected admission response")
	}
	reader := sp.reader(t.Context(), v.size)
	waited := false
	reader.onWait = func() {
		waited = true
		if w.status != 0 {
			t.Fatalf("headers committed before first block: %d", w.status)
		}
		if _, err := sp.WriteAt([]byte(payload), 0); err != nil {
			t.Fatal(err)
		}
	}
	serveStream(w, r, v, reader)
	if !waited || reader.err != nil || response.Code != 206 || response.Body.String() != payload[4:] {
		t.Fatal(waited, reader.err, response.Code, response.Body)
	}
}

func TestPlayerOwnerUpstreamTimeoutAbortsNormalResponse(t *testing.T) {
	s, _ := setup(t, nil)
	s.playerRole = vrclog.Owner
	headers := make(chan struct{})
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			select {
			case <-headers:
				return nil, context.DeadlineExceeded
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		})}
	}
	w := &playerHeaderWriter{ResponseRecorder: httptest.NewRecorder(), flushed: sync.OnceFunc(func() { close(headers) })}
	r := httptest.NewRequest("GET", videoURL(payload), nil)
	r.Header.Set("User-Agent", "NSPlayer/12")
	r.Header.Set("Range", "bytes=4-")
	var crash any
	func() { defer func() { crash = recover() }(); s.ServeHTTP(w, r) }()
	if crash != http.ErrAbortHandler || w.Code != 206 || w.Body.Len() != 0 || w.Header().Get("Content-Range") != "bytes 4-35/36" {
		t.Fatalf("status=%d body=%q crash=%v headers=%v", w.Code, w.Body.String(), crash, w.Header())
	}
}

type playerHeaderWriter struct {
	*httptest.ResponseRecorder
	flushed func()
}

func (w *playerHeaderWriter) Flush() { w.ResponseRecorder.Flush(); w.flushed() }

func TestFormalPlaybackLogLifecycle(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "output_log_2026-11-17_00-00-00.txt")
	if err := os.WriteFile(file, []byte("old log\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, _ := setup(t, nil)
	formalCatalog(t, s, video{key: strings.Repeat("f", 32), size: 36})
	if err := s.UpdatePrefetchQueue([]int64{2}, false); err != nil {
		t.Fatal(err)
	}
	tail := vrclog.Tail{Dir: dir}
	var timeline vrclog.PlayerTimeline
	poll := func(at time.Time) {
		t.Helper()
		events, err := tail.Poll(at)
		if err != nil {
			t.Fatal(err)
		}
		s.ObservePlaybackLog(&timeline, events, time.Now())
	}
	at := time.Now().Truncate(time.Second)
	poll(at.Add(-vrclog.PollInterval))
	now := at.Format("2006.01.02 15:04:05")
	lines := now + " Debug - [VideoQueueManager] [LogOtherPlayerAndDeviceType] isLocal = True, isOwner = False,\n" +
		now + " Debug - [VideoQueueManager] [LogOtherPlayerAndDeviceType] isLocal = True, isOwner = True,\n" +
		now + " Debug - [USharpVideo (WannaDance)] Started video: http://api.udon.dance/Api/Songs/play?id=1\n" +
		now + " Debug - [USharpVideo (WannaDance)] Syncing video to 50.00, current time = 0\n"
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(f, lines)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	// The poll starts just before the second stamped on the appended lines.
	poll(at.Add(-time.Millisecond))
	s.mu.Lock()
	active := s.formal != nil && s.formal.song == "1" && s.playerRole == vrclog.Owner
	queueStart := s.queueClockLocked(s.prefetchSources["2"], time.Now()).at
	s.mu.Unlock()
	if !active {
		t.Fatal("appended log not observed")
	}
	if want := at.Add(50 * time.Second); !queueStart.Equal(want) {
		t.Fatalf("queue start=%v after sync, want %v", queueStart, want)
	}
	if err := os.Truncate(file, 0); err != nil {
		t.Fatal(err)
	}
	poll(at.Add(2 * time.Second))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.formal != nil || s.playerRole != vrclog.UnknownRole {
		t.Fatal("truncation did not reset role/model")
	}
	current := time.Now()
	if got := s.queueClockLocked(s.prefetchSources["2"], current).at; got != current {
		t.Fatal("queue retained a future clock after log reset", got)
	}
}

func TestFormalMissingMetadataWaitsForSync(t *testing.T) {
	body := strings.Repeat("a", int(3*rangeBlockSize))
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		serveFixtureContent(w, r, body)
	}, func(c *Config) {
		c.ResolvePlayback = func(context.Context, string) (string, error) { return videoURL(body), nil }
	})
	v := video{songID: "1", key: videoKey(body), size: int64(len(body))}
	old := video{key: videoKey(payload), size: int64(len(payload))}
	formalCatalog(t, s, old)
	if _, err := s.Store.db.Exec("UPDATE songs SET end=NULL WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Second)
	timeline := vrclog.PlayerTimeline{Role: vrclog.Follower, Anchor: vrclog.PlaybackAnchor{SongID: 1, Started: at, At: at, Position: 10}}
	s.applyPlaybackTimeline(timeline, true)
	s.mu.Lock()
	active := s.formal != nil
	s.mu.Unlock()
	if active {
		t.Fatal("missing catalog duration produced a formal clock")
	}
	if err := s.Store.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: videoURL(body)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.db.Exec("UPDATE songs SET end=100 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	local, err := s.newLocalResource(v, s.cfg.Logger)
	if err != nil {
		t.Fatal(err)
	}
	defer local.storage.release()
	w := request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=1", map[string]string{"User-Agent": "NSPlayer/12", "Range": "bytes=0-0"})
	if w.Code != http.StatusPartialContent || w.Body.String() != "a" {
		t.Fatal("GET without a formal clock", w.Code, w.Body)
	}
	s.applyPlaybackTimeline(timeline, false)
	s.mu.Lock()
	active = s.formal != nil
	s.mu.Unlock()
	if active {
		t.Fatal("GET or idle poll activated a formal source")
	}
	timeline.Anchor.At = time.Now()
	timeline.Anchor.Position = 20
	s.applyPlaybackTimeline(timeline, true)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.formal == nil || s.formal.key != v.key || !s.formal.clock.at.Equal(timeline.Anchor.At.Add(-20*time.Second)) || s.useBlocks[useBlockID{v.key, 1}] != 1 {
		t.Fatal("new sync did not establish a timed source", s.formal)
	}
}

func TestFormalRejectedStartRechecksVersionAtNextSync(t *testing.T) {
	s, _ := setup(t, nil)
	old := video{key: videoKey(payload), size: 3 * s.cfg.BlockSize}
	formalCatalog(t, s, old)
	if err := s.Store.SetSongPolicy(t.Context(), 1, SongBlocked); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	timeline := vrclog.PlayerTimeline{Role: vrclog.Owner, Anchor: vrclog.PlaybackAnchor{SongID: 1, Started: at, At: at, CatalogStart: true}}
	s.applyPlaybackTimeline(timeline, true)
	current := strings.Repeat("b", int(old.size))
	if err := s.Store.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: videoURL(current)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.SetSongPolicy(t.Context(), 1, SongNormal); err != nil {
		t.Fatal(err)
	}
	timeline.Anchor.CatalogStart = false
	timeline.Anchor.Position = 20
	s.applyPlaybackTimeline(timeline, true)
	key := videoKey(current)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.formal == nil || s.formal.key != key || s.useBlocks[useBlockID{key, 1}] != 1 || s.useBlocks[useBlockID{old.key, 1}] != 0 {
		t.Fatal("sync failed to acquire the current version")
	}
}

func TestClearPlaybackLogReleasesOnlyItsSource(t *testing.T) {
	s, _ := setup(t, nil)
	v := video{key: videoKey(payload), size: 3 * s.cfg.BlockSize}
	formalCatalog(t, s, v)
	_, release, err := s.addDemand(t.Context(), v, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	now := time.Now()
	s.applyPlaybackTimeline(vrclog.PlayerTimeline{Role: vrclog.Owner, Anchor: vrclog.PlaybackAnchor{SongID: 1, Started: now, At: now, Position: 10}}, true)
	s.mu.Lock()
	shared := s.useBlocks[useBlockID{v.key, 1}] == 2
	s.mu.Unlock()
	if !shared {
		t.Fatal("expected a manual and a formal reference")
	}
	s.ClearPlaybackLog()
	s.mu.Lock()
	clean := s.formal == nil && s.playerRole == vrclog.UnknownRole && s.useBlocks[useBlockID{v.key, 1}] == 1
	s.mu.Unlock()
	if !clean {
		t.Fatal("playback cleanup left room state or disturbed the manual source")
	}
}

func TestFormalFollowerConfirmationBeforeSync(t *testing.T) {
	s, _ := setup(t, nil)
	v := video{key: videoKey(payload), songID: "1", size: 3 * s.cfg.BlockSize}
	formalCatalog(t, s, v)
	local, err := s.newLocalResource(v, s.cfg.Logger)
	if err != nil {
		t.Fatal(err)
	}
	defer local.storage.release()
	at := time.Now().Truncate(time.Second)
	timeline := vrclog.PlayerTimeline{Role: vrclog.Owner}
	observe := func(e vrclog.PlaybackEvent) {
		e.At = at
		s.ObservePlaybackLog(&timeline, []vrclog.Event{{Playback: &e}}, at)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Range", "bytes=0-")
	r = r.WithContext(context.WithValue(r.Context(), playbackClockKey{}, &playbackClock{limit: v.size}))
	observe(vrclog.PlaybackEvent{Kind: "start", SongID: 1})
	if got := s.playerAdmission(r, v); got.role != vrclog.Owner || got.reject || !timeline.Anchor.CatalogStart {
		t.Fatal("expected an owner start", got, timeline.Anchor)
	}
	owner := false
	observe(vrclog.PlaybackEvent{Kind: "role", Owner: &owner})
	if got := s.playerAdmission(r, v); got.role != vrclog.Follower || got.reject {
		t.Fatal("follower rejected from an inferred catalog start", got)
	}
	s.mu.Lock()
	cleared := s.formal == nil && len(s.useBlocks) == 0
	s.mu.Unlock()
	if !cleared {
		t.Fatal("inferred start retained block demand")
	}
	observe(vrclog.PlaybackEvent{Kind: "sync", Position: 10})
	anchor := timeline.Anchor
	observe(vrclog.PlaybackEvent{Kind: "role", Owner: &owner})
	if !s.playerAdmission(r, v).reject || anchor.SongID != 1 || timeline.Anchor != anchor {
		t.Fatal("follower lost the routed sync", timeline.Anchor)
	}
	s.applyPlaybackTimeline(vrclog.PlayerTimeline{}, true)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.formal != nil || s.playerRole != vrclog.UnknownRole || len(s.useBlocks) != 0 {
		t.Fatal("room exit retained playback")
	}
}

func TestFormalSameSecondStopAndRestart(t *testing.T) {
	s, _ := setup(t, nil)
	a := video{key: videoKey(payload), size: 3 * s.cfg.BlockSize}
	formalCatalog(t, s, a)
	at := time.Now().Truncate(time.Second)
	owner := true
	start := vrclog.Event{Playback: &vrclog.PlaybackEvent{Kind: "start", At: at, SongID: 1, Owner: &owner}}
	stop := vrclog.Event{Playback: &vrclog.PlaybackEvent{Kind: "stop", At: at, SongID: 1}}
	var timeline vrclog.PlayerTimeline
	s.ObservePlaybackLog(&timeline, []vrclog.Event{start}, at)
	first := timeline.Anchor
	body := strings.Repeat("b", int(a.size))
	if err := s.Store.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: videoURL(body)}); err != nil {
		t.Fatal(err)
	}
	s.ObservePlaybackLog(&timeline, []vrclog.Event{start}, at)
	s.mu.Lock()
	same := s.formal != nil && s.formal.key == a.key
	s.mu.Unlock()
	if !same {
		t.Fatal("duplicate callback replaced the bound version")
	}
	s.ObservePlaybackLog(&timeline, []vrclog.Event{stop, start}, at)
	s.mu.Lock()
	defer s.mu.Unlock()
	if timeline.Anchor != first || s.formal == nil || s.formal.key != videoKey(body) || s.useBlocks[useBlockID{a.key, 1}] != 0 {
		t.Fatal("stop and restart reused the old binding")
	}
}

func TestFormalCapacityExpansionRequiresAllowedSync(t *testing.T) {
	for _, held := range []int64{0, 2} {
		t.Run(fmt.Sprintf("held_%d", held), func(t *testing.T) {
			s, _ := setup(t, nil)
			v := video{key: videoKey(payload), size: 4 * s.cfg.BlockSize}
			formalCatalog(t, s, v)
			at := time.Now()
			timeline := vrclog.PlayerTimeline{Anchor: vrclog.PlaybackAnchor{SongID: 1, At: at, Started: at, Position: 40}}
			if held > 0 {
				s.applyPlaybackTimeline(timeline, true)
			}
			full := useSpan{"occupant", 0, backgroundUseBlocks - held}
			s.mu.Lock()
			err := s.reserveUseLocked(full, false)
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			timeline.Anchor.Position = 10
			s.applyPlaybackTimeline(timeline, true)
			check := func(count int64) {
				t.Helper()
				s.mu.Lock()
				defer s.mu.Unlock()
				for b := int64(0); b < 4; b++ {
					want := 0
					if b >= 4-count {
						want = 1
					}
					if got := s.useBlocks[useBlockID{v.key, b}]; got != want {
						t.Fatalf("block %d has %d references, want %d", b, got, want)
					}
				}
			}
			check(held)
			s.mu.Lock()
			if s.formal == nil {
				s.mu.Unlock()
				t.Fatal("capacity refusal lost the room clock")
			}
			s.releaseUseLocked(full, time.Now())
			s.mu.Unlock()
			s.applyPlaybackTimeline(timeline, false)
			check(held)
			if err := s.Store.SetSongPolicy(t.Context(), 1, SongBlocked); err != nil {
				t.Fatal(err)
			}
			timeline.Anchor.Position = 11
			s.applyPlaybackTimeline(timeline, true)
			check(held)
			s.mu.Lock()
			corrected := s.formal != nil && s.formal.clock.at.Equal(at.Add(-11*time.Second))
			s.mu.Unlock()
			if !corrected {
				t.Fatal("refused expansion lost the corrected room clock")
			}
			if err := s.Store.SetSongPolicy(t.Context(), 1, SongNormal); err != nil {
				t.Fatal(err)
			}
			timeline.Anchor.Position = 12
			s.applyPlaybackTimeline(timeline, true)
			check(3)
		})
	}
}

func TestFormalSyncAtEndRechecksVersionOnCorrection(t *testing.T) {
	for _, samePoll := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_poll_%t", samePoll), func(t *testing.T) {
			s, _ := setup(t, nil)
			old := video{key: videoKey(payload), size: 3 * s.cfg.BlockSize}
			formalCatalog(t, s, old)
			at := time.Now().Truncate(time.Second)
			var timeline vrclog.PlayerTimeline
			syncTo := func(position float64) vrclog.Event {
				return vrclog.Event{Playback: &vrclog.PlaybackEvent{Kind: "sync", At: at, Position: position}}
			}
			route := vrclog.Event{Playback: &vrclog.PlaybackEvent{Kind: "route", SongID: 1, At: at}}
			s.ObservePlaybackLog(&timeline, []vrclog.Event{route, syncTo(10)}, at)
			body := strings.Repeat("b", int(old.size))
			if err := s.Store.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 1, Node: "cf", URL: videoURL(body)}); err != nil {
				t.Fatal(err)
			}
			if samePoll {
				s.ObservePlaybackLog(&timeline, []vrclog.Event{syncTo(100.3), syncTo(0.16)}, at)
			} else {
				s.ObservePlaybackLog(&timeline, []vrclog.Event{syncTo(100)}, at)
				s.mu.Lock()
				ended := s.formal == nil && len(s.useBlocks) == 0
				s.mu.Unlock()
				if !ended {
					t.Fatal("sync at the end retained playback")
				}
				s.ObservePlaybackLog(&timeline, []vrclog.Event{syncTo(0.16)}, at)
			}
			key := videoKey(body)
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.formal == nil || s.formal.key != key || s.useBlocks[useBlockID{key, 1}] != 1 || s.useBlocks[useBlockID{old.key, 1}] != 0 {
				t.Fatal("in-bounds correction failed to acquire the current version")
			}
		})
	}
}
