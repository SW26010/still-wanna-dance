package console

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

func monitorPlaybackResponse() upstreamstate.PlaybackURLResponse {
	at := time.Now().Add(-time.Minute)
	return upstreamstate.PlaybackURLResponse{
		SongID: 1, API: "https://api.udon.dance/Api/Songs/play", Node: "cf",
		URL:            "https://play.udon.dance/files/1/42-video.mp4?e=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb&s=1234&token=private",
		QueryStartedAt: at, ObservedAt: at.Add(time.Second),
	}
}

func TestMonitorPlaybackURLPersistsMetadataWithoutDownload(t *testing.T) {
	c := testConsole(t)
	if err := os.Remove(libraryPaths(c.settings).VideoDir); err != nil {
		t.Fatal(err)
	}
	c.client.Transport = catalogTransport(func(*http.Request) (*http.Response, error) {
		t.Error("URL persistence made an extra request")
		return nil, errors.New("unexpected request")
	})
	ctx := context.Background()
	c.syncMonitorCatalogs(ctx, []upstreamstate.CatalogResponse{monitorFull("kiva", "20261015000000", "original", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")})
	old := monitorPlaybackResponse()
	c.syncMonitorPlaybackURL(ctx, old)
	newer := old
	newer.QueryStartedAt = old.QueryStartedAt.Add(2 * time.Second)
	newer.ObservedAt = newer.QueryStartedAt.Add(time.Second)
	newer.URL = "https://play.udon.dance/files/1/42-new.mp4?e=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb&s=5678&token=new"
	c.syncMonitorPlaybackURL(ctx, newer)
	old.ObservedAt = newer.ObservedAt.Add(time.Second) // Earlier request finishes last.
	c.syncMonitorPlaybackURL(ctx, old)
	shared := newer
	shared.SongID = 2
	c.syncMonitorPlaybackURL(ctx, shared)
	db, err := sql.Open("sqlite", filepath.Join(testStorageRoot(c.settings), "storage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var size sql.NullInt64
	var mapped string
	if err := db.QueryRow("SELECT md5,byte_size FROM song_media JOIN media USING(md5) WHERE song_id=1").Scan(&mapped, &size); err != nil || mapped != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || size.Valid {
		t.Fatalf("URL changed catalog mapping or unrelated metadata: %s, %+v, %v", mapped, size, err)
	}
	if err := db.QueryRow("SELECT byte_size FROM media WHERE md5='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'").Scan(&size); err != nil || !size.Valid || size.Int64 != 5678 {
		t.Fatalf("URL metadata not saved: %+v, %v", size, err)
	}
	if err := db.QueryRow("SELECT md5 FROM song_media WHERE song_id=2").Scan(&mapped); err != nil || mapped != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("URL did not fill the missing mapping: %s, %v", mapped, err)
	}
	for _, id := range []int64{1, 2} {
		urls, err := c.store.SongURLs(ctx, id, "cf")
		if err != nil || len(urls) != 1 || urls[0].URL != newer.URL || !urls[0].QueryStartedAt.Equal(newer.QueryStartedAt) || !urls[0].ObservedAt.Equal(newer.ObservedAt) {
			t.Fatalf("wrong persisted observation for %d: %+v, %v", id, urls, err)
		}
	}
	for table, want := range map[string]int{"songs": 2, "song_media": 2, "song_urls": 2, "media": 2, "song_usage": 0, "request_events": 0, "request_observation": 0} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count=%d, want %d: %v", table, count, want, err)
		}
	}
	if c.service != nil {
		t.Fatal("metadata persistence started a video engine")
	}
	for _, dir := range []string{"videos", "tmp"} {
		entries, err := os.ReadDir(filepath.Join(testStorageRoot(c.settings), dir))
		if err != nil && !os.IsNotExist(err) || len(entries) != 0 {
			t.Fatalf("persistence created media files in %s: %v, %v", dir, entries, err)
		}
	}
}

func TestMonitorPlaybackURLCanceledOrClosingDoesNotOpenStorage(t *testing.T) {
	for _, closing := range []bool{false, true} {
		c := testConsole(t)
		ctx, cancel := context.WithCancel(context.Background())
		if closing {
			c.closing = true
		} else {
			cancel()
		}
		c.syncMonitorPlaybackURL(ctx, monitorPlaybackResponse())
		cancel()
		if c.service != nil {
			t.Fatal("opened storage for canceled/closing response")
		}
		if urls, err := c.store.SongURLs(context.Background(), 42, "cf"); err != nil || len(urls) != 0 {
			t.Fatal("canceled/closing response wrote metadata", urls, err)
		}
		c.closing = false
	}
}

func TestMonitorPlaybackURLStorageFailureRollsBack(t *testing.T) {
	c := testConsole(t)
	ctx := context.Background()
	old := monitorPlaybackResponse()
	c.syncMonitorPlaybackURL(ctx, old) // Playback-only checks write the application store without a media engine.
	db, err := sql.Open("sqlite", filepath.Join(testStorageRoot(c.settings), "storage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TRIGGER fail_probe_metadata BEFORE INSERT ON media BEGIN SELECT RAISE(FAIL, 'test storage failure'); END"); err != nil {
		t.Fatal(err)
	}
	newer := old
	newer.QueryStartedAt = old.QueryStartedAt.Add(2 * time.Second)
	newer.ObservedAt = newer.QueryStartedAt.Add(time.Second)
	newer.URL = "https://play.udon.dance/files/1/42-new.mp4?e=cccccccccccccccccccccccccccccccc&s=5678"
	c.syncMonitorPlaybackURL(ctx, newer)
	if !errors.Is(c.store.Error(), cacheproxy.ErrLocalStorage) {
		t.Fatal("monitor persistence did not retain storage fault", c.store.Error())
	}
	urls, err := c.store.SongURLs(ctx, old.SongID, old.Node)
	if err != nil || len(urls) != 1 || urls[0].URL != old.URL {
		t.Fatalf("failed update was not rolled back: %+v, %v", urls, err)
	}
	var size int64
	if err := db.QueryRow("SELECT byte_size FROM media").Scan(&size); err != nil || size != 1234 {
		t.Fatalf("failed update changed size: %d, %v", size, err)
	}
}

func TestMonitorMetadataBackfillFollowsRetentionOrder(t *testing.T) {
	c := testConsole(t)
	keys := map[int64]string{1981: strings.Repeat("a", 32), 5404: strings.Repeat("b", 32)}
	var mu sync.Mutex
	var requested []int64
	var calls, resourceCalls int
	transport := catalogTransport(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}
		switch r.URL.Path {
		case "/api/v2/wanna/songs", "/api/wannaInfo":
			resp.Body = io.NopCloser(strings.NewReader(fmt.Sprintf(`{"code":200,"data":{"time":"20261015000000","groups":[{"entries":[{"id":1981,"checksum":%q},{"id":5404,"checksum":%q}]}]}}`, keys[1981], keys[5404])))
		case "/Api/Songs/list":
			// The ordinary sample is deliberately absent from the local MD5 list.
			resp.Body = io.NopCloser(strings.NewReader(`{"time":"20261015000000","groups":{"contents":[{"songInfos":[{"id":42}]}]}}`))
		case "/Api/Songs/play":
			id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
			mu.Lock()
			requested = append(requested, id)
			mu.Unlock()
			key := keys[id]
			if key == "" {
				key = strings.Repeat("c", 32)
			}
			host := "play.udon.dance"
			if r.URL.Query().Get("node") == "nya" {
				host = "nya.xin.moe"
			}
			resp.StatusCode = http.StatusFound
			resp.Header.Set("Location", fmt.Sprintf("https://%s/files/1/%d-video.mp4?e=%s&s=4", host, id, key))
		default:
			mu.Lock()
			resourceCalls++
			mu.Unlock()
			resp.StatusCode = http.StatusPartialContent
			resp.Header.Set("Content-Range", "bytes 0-3/4")
			resp.Body = io.NopCloser(strings.NewReader("abcd"))
		}
		return resp, nil
	})
	c.requestRevision = upstreamrequest.Default.Publish(monitorTestChannels{transport})
	if err := c.StartUpstreamMonitor(); err != nil {
		t.Fatal(err)
	}
	waitMonitor(t, c)
	assertRequests := func(want int64, wantCalls int) {
		t.Helper()
		if !slices.Equal(requested, []int64{want, want}) || calls != wantCalls || resourceCalls != 2 {
			t.Fatalf("requested=%v calls=%d resourceCalls=%d, want song=%d total=%d", requested, calls, resourceCalls, want, wantCalls)
		}
	}
	assertRequests(1981, 10)
	if pending := c.monitorPlaybackSongCandidates(context.Background()); !slices.Equal(pending, []int64{5404}) {
		t.Fatal("completed song stayed pending", pending)
	}
	requested = nil
	if err := c.monitor.CheckSelected(context.Background(), upstreamstate.CheckPlayback); err != nil {
		t.Fatal(err)
	}
	assertRequests(5404, 12)
	if pending := c.monitorPlaybackSongCandidates(context.Background()); len(pending) != 0 {
		t.Fatal("metadata still incomplete", pending)
	}
	requested = nil
	if err := c.monitor.CheckSelected(context.Background(), upstreamstate.CheckPlayback); err != nil {
		t.Fatal(err)
	}
	assertRequests(42, 14)
}

func TestMonitorMetadataCandidatesDoNotOpenOrReviveStorage(t *testing.T) {
	c := testConsole(t)
	if got := c.monitorPlaybackSongCandidates(context.Background()); len(got) != 0 || c.service != nil {
		t.Fatal("selection initialized an empty library", got)
	}
	c.syncMonitorCatalogs(context.Background(), []upstreamstate.CatalogResponse{monitorFull("kiva", "20261015000000", "song", strings.Repeat("a", 32))})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := c.monitorPlaybackSongCandidates(ctx); len(got) != 0 {
		t.Fatal("selection ignored cancellation", got)
	}
	c.closing = true
	got := c.monitorPlaybackSongCandidates(context.Background())
	c.closing = false
	if len(got) != 0 {
		t.Fatal("selection ignored shutdown", got)
	}
}
