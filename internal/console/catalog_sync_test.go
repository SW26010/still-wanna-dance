package console

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamrequest"
)

func TestCatalogSyncDirectFallbackPreservesMissingSong(t *testing.T) {
	const missing int64 = 900001
	for _, failure := range []string{"disconnect", "http_status", "invalid_body"} {
		t.Run(failure, func(t *testing.T) {
			c := testConsole(t)
			ctx := t.Context()
			if err := c.store.SyncCatalog(ctx, cacheproxy.Catalog{Revision: "20251031000000", Source: cacheproxy.UdonCatalogURL, Songs: []cacheproxy.CatalogSong{{ID: 42}, {ID: missing}}}); err != nil {
				t.Fatal(err)
			}
			address := func(id int64) string {
				return fmt.Sprintf("https://play.udon.dance/files/1/%d-video.mp4?e=%032x&s=1234", id, id)
			}
			if err := c.store.ObserveSongURL(ctx, cacheproxy.SongURL{SongID: 42, URL: address(42), API: "https://api.udon.dance/Api/Songs/play", Node: "cf", QueryStartedAt: time.Now(), ObservedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			var rejected, catalogs, playback, resources atomic.Int32
			good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Api/Songs/play":
					playback.Add(1)
					if r.URL.Query().Get("id") != fmt.Sprint(missing) {
						t.Error("retried a sampled song instead of the missing metadata", r.URL.Query())
					}
					w.Header().Set("Location", address(missing))
					w.WriteHeader(http.StatusFound)
				case "/Api/Songs/list":
					catalogs.Add(1)
					io.WriteString(w, `{"time":"20261031000000","groups":{"contents":[{"songInfos":[{"id":42}]}]}}`)
				case "/api/v2/wanna/songs", "/api/wannaInfo":
					catalogs.Add(1)
					io.WriteString(w, `{"code":200,"data":{"time":"20261031000000","groups":[{"entries":[{"id":42,"checksum":"0000000000000000000000000000002a"}]}]}}`)
				default:
					resources.Add(1)
					if r.Header.Get("Range") != "bytes=0-0" {
						t.Error("resource check exceeded lightweight scope", r.Header)
					}
					w.Header().Set("Content-Range", "bytes 0-0/1234")
					w.WriteHeader(http.StatusPartialContent)
					io.WriteString(w, "x")
				}
			}))
			defer good.Close()
			healthy := good.Client().Transport.(*http.Transport).Clone()
			healthy.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, good.Listener.Addr().String())
			}
			defer healthy.CloseIdleConnections()
			failed := healthy.Clone()
			defer failed.CloseIdleConnections()
			if failure == "disconnect" {
				// TCP succeeds, then the peer closes during HTTP exchange.
				bad, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer bad.Close()
				go func() {
					for {
						conn, err := bad.Accept()
						if err != nil {
							return
						}
						rejected.Add(1)
						conn.Close()
					}
				}()
				failed.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, bad.Addr().String())
				}
			} else {
				bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					rejected.Add(1)
					if failure == "http_status" {
						w.WriteHeader(http.StatusServiceUnavailable)
					} else if r.URL.Path == "/Api/Songs/play" {
						w.Header().Set("Location", "https://play.udon.dance/not-a-video")
						w.WriteHeader(http.StatusFound)
					} else {
						io.WriteString(w, `{"invalid":true}`)
					}
				}))
				defer bad.Close()
				failed.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, bad.Listener.Addr().String())
				}
			}
			c.client.Transport = &coldBusinessChannels{RoundTripper: failed, channels: []upstreamrequest.Candidate{
				{ID: "direct/fixture/::1", Mode: "direct", Transport: failed},
				{ID: "direct/fixture/127.0.0.1", Mode: "direct", Transport: healthy},
			}}
			// Use the production application adapter without starting Monitor.
			c.mu.Lock()
			err := c.startAutomaticConnectivityLocked()
			c.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			waitCatalogSync(t, c)
			urls, err := c.store.SongURLs(ctx, missing, "cf")
			if err != nil || len(urls) != 1 || urls[0].URL != address(missing) {
				t.Fatalf("missing metadata was not stored: urls=%v err=%v", urls, err)
			}
			status, err := c.store.CatalogStatus(ctx)
			wantRejected := int32(3)
			if err != nil || status.Revision != "20261031000000" || catalogs.Load() != 1 || playback.Load() != 1 || rejected.Load() != wantRejected {
				t.Fatalf("catalog=%+v err=%v catalog requests=%d playback=%d rejected=%d", status, err, catalogs.Load(), playback.Load(), rejected.Load())
			}
			if resources.Load() != 1 {
				t.Fatal("resource check did not reuse the resolved URL", resources.Load())
			}
			if c.monitor != nil || c.service != nil {
				t.Fatal("metadata synchronization required Monitor or the media engine")
			}
		})
	}
}
