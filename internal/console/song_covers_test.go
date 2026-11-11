package console

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

func coverJPEG(t *testing.T) []byte {
	t.Helper()
	var body bytes.Buffer
	if err := jpeg.Encode(&body, image.NewRGBA(image.Rect(0, 0, 480, 360)), nil); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func coverRequest(c *Console, method, query, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+c.address+"/api/library/cover?"+query, nil)
	r.Header.Set("X-SWD-Token", token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	return w
}

func TestSongCoverOnDemandCacheAndAdmission(t *testing.T) {
	c := testConsole(t)
	body := coverJPEG(t)
	var calls atomic.Int32
	c.client.Transport = catalogTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.String() != "https://aya.kiva.moe/images/small18.jpg" || r.Method != "GET" {
			t.Errorf("unexpected artwork request: %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	dir := filepath.Join(c.files().Root, "data", "covers")
	if _, err := os.Stat(dir); !os.IsNotExist(err) || calls.Load() != 0 {
		t.Fatal("startup initialized artwork", err, calls.Load())
	}
	for _, tc := range []struct {
		method, query, token string
		status               int
	}{
		{"GET", "id=18", "", 403}, {"POST", "id=18", c.token, 405},
		{"HEAD", "id=18", c.token, 405}, {"GET", "id=../18", c.token, 400},
		{"GET", "id=0", c.token, 400}, {"GET", "id=018", c.token, 400},
		{"GET", "id=18&id=2", c.token, 400}, {"GET", "id=18&url=https://evil.invalid", c.token, 400},
	} {
		if w := coverRequest(c, tc.method, tc.query, tc.token); w.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatal("rejected request fetched artwork")
	}
	for range 2 {
		w := coverRequest(c, "GET", "id=18", c.token)
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(w.Body.Bytes(), body) {
			t.Fatal(w.Code, w.Header(), w.Body.Len())
		}
	}
	if calls.Load() != 1 {
		t.Fatal("cache missed", calls.Load())
	}
	data, err := os.ReadFile(filepath.Join(dir, "18.jpg"))
	if err != nil || !bytes.Equal(data, body) {
		t.Fatal("cache contents", err)
	}
	// A new cache instance uses the file without any network availability.
	var restarted songCoverCache
	defer restarted.close()
	client := &http.Client{Transport: catalogTransport(func(*http.Request) (*http.Response, error) {
		t.Error("restart refetched cached artwork")
		return nil, fmt.Errorf("offline")
	})}
	if _, status, err := restarted.read(t.Context(), client, dir, "18", ""); status != 200 || err != nil {
		t.Fatal(status, err)
	}
	if c.service != nil || c.httpServer != nil || c.batch.Running || c.queue.snapshot().Running || c.resources.IsResourceDomain("aya.kiva.moe") {
		t.Fatal("cover started video/channel work")
	}
}

func TestSongCoverFailureIsNotCachedAndCanRecover(t *testing.T) {
	for _, failure := range []string{"404", "redirect", "html", "truncated", "oversized"} {
		t.Run(failure, func(t *testing.T) {
			c := testConsole(t)
			body := coverJPEG(t)
			var calls int
			c.client.Transport = catalogTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				status, data := 200, body
				if calls == 1 {
					switch failure {
					case "404":
						status, data = 404, []byte("not found")
					case "redirect":
						status, data = 302, nil
					case "html":
						data = []byte("<html>unavailable</html>")
					case "truncated":
						data = body[:len(body)/2]
					case "oversized":
						data = bytes.Repeat([]byte("x"), maxSongCoverBytes+1)
					}
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://evil.invalid/cover"}}, Body: io.NopCloser(bytes.NewReader(data))}, nil
			})
			w := coverRequest(c, "GET", "id=18", c.token)
			if w.Code < 400 {
				t.Fatal("accepted invalid image", w.Code)
			}
			filename := filepath.Join(c.files().Root, "data", "covers", "18.jpg")
			if _, err := os.Stat(filename); !os.IsNotExist(err) {
				t.Fatal("failure persisted", err)
			}
			w = coverRequest(c, "GET", "id=18", c.token)
			if w.Code != 200 || calls != 2 {
				t.Fatal("explicit retry did not recover", w.Code, calls)
			}
		})
	}
}

func TestSongCoverSharedRefresh(t *testing.T) {
	for _, status := range []int{200, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			dir, body := t.TempDir(), coverJPEG(t)
			if err := os.WriteFile(filepath.Join(dir, "18.jpg"), body, 0600); err != nil {
				t.Fatal(err)
			}
			synctest.Test(t, func(t *testing.T) {
				var cache songCoverCache
				defer cache.close()
				var calls atomic.Int32
				entered, release := make(chan struct{}), make(chan struct{})
				client := &http.Client{Transport: catalogTransport(func(*http.Request) (*http.Response, error) {
					if calls.Add(1) == 1 {
						close(entered)
					}
					<-release
					return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
				})}
				var wg sync.WaitGroup
				read := func() {
					data, gotStatus, err := cache.read(t.Context(), client, dir, "18", "refresh")
					if gotStatus != status || (err == nil) != (status == 200) || status == 200 && !bytes.Equal(data, body) {
						t.Errorf("refresh result: status=%d bytes=%d err=%v", gotStatus, len(data), err)
					}
				}
				wg.Go(read)
				<-entered
				for range 2 {
					wg.Go(read)
				}
				// All three requests are blocked: one upstream and two waiting
				// for its result. No scheduling delay or real-time sleep is needed.
				synctest.Wait()
				close(release)
				wg.Wait()
				if calls.Load() != 1 {
					t.Fatalf("overlapping refreshes fetched %d times, want 1", calls.Load())
				}
				data, gotStatus, err := cache.read(t.Context(), client, dir, "18", "refresh")
				if gotStatus != 200 || err != nil || !bytes.Equal(data, body) || calls.Load() != 1 {
					t.Fatal("later refresh repeated the session check", gotStatus, err, calls.Load())
				}
			})
		})
	}
}

func TestSongCoverSharedRefreshCancellation(t *testing.T) {
	for _, canceled := range []string{"owner", "waiter", "shutdown"} {
		t.Run(canceled, func(t *testing.T) {
			dir, body := t.TempDir(), coverJPEG(t)
			if err := os.WriteFile(filepath.Join(dir, "18.jpg"), body, 0600); err != nil {
				t.Fatal(err)
			}
			synctest.Test(t, func(t *testing.T) {
				var cache songCoverCache
				defer cache.close()
				var calls atomic.Int32
				entered, release := make(chan struct{}), make(chan struct{})
				client := &http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
					if calls.Add(1) == 1 {
						close(entered)
						select {
						case <-release:
						case <-r.Context().Done():
							return nil, r.Context().Err()
						}
					}
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
				})}
				ownerCtx, cancelOwner := context.WithCancel(t.Context())
				defer cancelOwner()
				waiterCtx, cancelWaiter := context.WithCancel(t.Context())
				defer cancelWaiter()
				owner, waiter := make(chan error, 1), make(chan error, 1)
				read := func(ctx context.Context, result chan<- error) {
					data, status, err := cache.read(ctx, client, dir, "18", "refresh")
					if err == nil && (status != 200 || !bytes.Equal(data, body)) {
						t.Errorf("bad successful result: status=%d bytes=%d", status, len(data))
					}
					result <- err
				}
				go read(ownerCtx, owner)
				<-entered
				go read(waiterCtx, waiter)
				synctest.Wait()
				switch canceled {
				case "owner":
					cancelOwner()
					if err := <-owner; err == nil {
						t.Fatal("canceled owner succeeded")
					}
					if err := <-waiter; err != nil {
						t.Fatal("live waiter did not take over", err)
					}
					if calls.Load() != 2 {
						t.Fatal("owner cancellation did not produce one replacement request", calls.Load())
					}
				case "waiter":
					cancelWaiter()
					if err := <-waiter; err != context.Canceled {
						t.Fatal("waiter cancellation", err)
					}
					close(release)
					if err := <-owner; err != nil {
						t.Fatal("waiter canceled the owner", err)
					}
					if calls.Load() != 1 {
						t.Fatal("waiter cancellation fetched again", calls.Load())
					}
				case "shutdown":
					cache.close()
					if err := <-owner; err == nil {
						t.Fatal("shutdown owner succeeded")
					}
					if err := <-waiter; err != context.Canceled {
						t.Fatal("shutdown left waiter active", err)
					}
					if calls.Load() != 1 {
						t.Fatal("shutdown started a replacement request", calls.Load())
					}
				}
			})
		})
	}
}

func TestSongCoverConcurrentRequestsAndShutdown(t *testing.T) {
	c := testConsole(t)
	body, entered, release := coverJPEG(t), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	c.client.Transport = catalogTransport(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if w := coverRequest(c, "GET", "id=18", c.token); w.Code != 200 {
				t.Error(w.Code, w.Body.String())
			}
		})
	}
	<-entered
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("concurrent cache misses", calls.Load())
	}
	entered = make(chan struct{})
	if err := os.WriteFile(filepath.Join(c.files().Root, "data", "covers", "19.jpg"), body, 0600); err != nil {
		t.Fatal(err)
	}
	c.client.Transport = catalogTransport(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	done := make(chan struct{})
	go func() { defer close(done); coverRequest(c, "GET", "id=19&mode=refresh", c.token) }()
	<-entered
	if w := coverRequest(c, "GET", "id=19&mode=cached", c.token); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatal("cached display waited for refresh", w.Code)
	}
	c.covers.close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown left a cover request running")
	}
}

type coverResolver struct {
	directResolver
	target string
	calls  atomic.Int32
}

func (d *coverResolver) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if address != "aya.kiva.moe:443" {
		return nil, fmt.Errorf("unexpected DNS target: %s", address)
	}
	d.calls.Add(1)
	return (&net.Dialer{}).DialContext(ctx, network, d.target)
}

func TestSongCoverUsesActiveDNSOrProxyWithoutVideoCandidates(t *testing.T) {
	for _, proxy := range []bool{false, true} {
		t.Run(fmt.Sprint("proxy=", proxy), func(t *testing.T) {
			c := testConsole(t)
			body := coverJPEG(t)
			cert, roots := relayCertificate(t)
			backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "aya.kiva.moe" || r.TLS.ServerName != "aya.kiva.moe" || r.URL.Path != "/images/small18.jpg" {
					t.Error("changed request identity", r.Host, r.TLS.ServerName, r.URL.Path)
				}
				w.Write(body)
			}))
			backend.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
			backend.StartTLS()
			defer backend.Close()
			dns := &coverResolver{directResolver: c.dns, target: backend.Listener.Addr().String()}
			c.dns = dns
			s := c.settings
			var proxyCalls atomic.Int32
			if proxy {
				s.UpstreamMode, s.ProxyBackend = "proxy", "external-socks5"
				s.SOCKS5Address = socksPeer(t, func(conn net.Conn, target string) {
					if target != "aya.kiva.moe:443" {
						t.Error("wrong proxy target", target)
						return
					}
					proxyCalls.Add(1)
					peer, err := net.DialTimeout("tcp", backend.Listener.Addr().String(), time.Second)
					if err != nil {
						t.Error(err)
						return
					}
					defer peer.Close()
					socksReply(conn, 0)
					done := make(chan struct{})
					go func() { io.Copy(peer, conn); peer.Close(); close(done) }()
					io.Copy(conn, peer)
					conn.Close()
					<-done
				})
			}
			_, client, err := c.networkFor(s)
			if err != nil {
				t.Fatal(err)
			}
			if proxy {
				client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots}
			} else {
				client.Transport.(*upstreamrequest.Transport).RoundTripper.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots}
			}
			c.client.CloseIdleConnections()
			c.client = client
			if w := coverRequest(c, "GET", "id=18", c.token); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			if proxy && (proxyCalls.Load() != 1 || dns.calls.Load() != 0) || !proxy && (dns.calls.Load() != 1 || proxyCalls.Load() != 0) {
				t.Fatal("wrong network path", dns.calls.Load(), proxyCalls.Load())
			}
			if c.resources.IsResourceDomain("aya.kiva.moe") {
				t.Fatal("image registered as video resource")
			}
		})
	}
}

func TestSongCoverCacheFailureDoesNotOverwriteOtherFiles(t *testing.T) {
	c := testConsole(t)
	dir := filepath.Join(c.files().Root, "data", "covers")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "18.jpg"), 0700); err != nil {
		t.Fatal(err)
	}
	c.client.Transport = catalogTransport(func(*http.Request) (*http.Response, error) {
		t.Error("unsafe cache reached upstream")
		return nil, fmt.Errorf("unexpected")
	})
	if w := coverRequest(c, "GET", "id=18", c.token); w.Code != 500 || !strings.Contains(w.Body.String(), "缓存") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestSongCoverCachedReadsAndRefresh(t *testing.T) {
	c := testConsole(t)
	body := coverJPEG(t)
	dir := filepath.Join(c.files().Root, "data", "covers")
	var calls int
	c.client.Transport = catalogTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	if w := coverRequest(c, "GET", "id=18&mode=cached", c.token); w.Code != 404 || calls != 0 {
		t.Fatal("local read fetched", w.Code, calls)
	}
	if w := coverRequest(c, "GET", "id=18", c.token); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	filename := filepath.Join(dir, "18.jpg")
	before, _ := os.Stat(filename)
	if w := coverRequest(c, "GET", "id=18&mode=refresh", c.token); w.Code != 200 || calls != 1 {
		t.Fatal(w.Code, calls)
	}
	after, _ := os.Stat(filename)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("unchanged image rewritten")
	}
	if err := os.WriteFile(filepath.Join(dir, "19.jpg"), body, 0600); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://"+c.address+"/api/library/covers?ids=18,19", nil)
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"cached":["18","19"]`) || !strings.Contains(w.Body.String(), `"refresh":["19"]`) || calls != 1 {
		t.Fatal("inspection fetched", w.Code, w.Body.String(), calls)
	}
	var changed bytes.Buffer
	jpeg.Encode(&changed, image.NewRGBA(image.Rect(0, 0, 240, 180)), nil)
	body = changed.Bytes()
	if w := coverRequest(c, "GET", "id=18&mode=refresh", c.token); w.Code != 200 || bytes.Equal(w.Body.Bytes(), body) || calls != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	// A new program session may check an existing file again.
	var restarted songCoverCache
	defer restarted.close()
	if _, status, err := restarted.read(t.Context(), c.client, dir, "18", "refresh"); status != 200 || err != nil || calls != 2 {
		t.Fatal("new session did not check", status, err, calls)
	}
	data, _ := os.ReadFile(filename)
	if !bytes.Equal(data, body) {
		t.Fatal("changed image not published")
	}
	c.client.Transport = catalogTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("missing"))}, nil
	})
	var offline songCoverCache
	defer offline.close()
	if _, status, err := offline.read(t.Context(), c.client, dir, "18", "refresh"); status != 404 || err == nil || calls != 3 {
		t.Fatal("new session did not try update", status, err, calls)
	}
	if _, status, err := offline.read(t.Context(), c.client, dir, "18", "refresh"); status != 200 || err != nil || calls != 3 {
		t.Fatal("failed update was retried in the same session", status, err, calls)
	}
	data, _ = os.ReadFile(filename)
	if !bytes.Equal(data, body) {
		t.Fatal("failed refresh erased cached image")
	}
	// Losing the file must still allow a download after a completed check.
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	c.client.Transport = catalogTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	if w := coverRequest(c, "GET", "id=18", c.token); w.Code != 200 || calls != 4 || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatal("missing file was suppressed by the session check", w.Code, calls)
	}
}

func TestSongCoverLimitsAllConcurrentDownloadsToFour(t *testing.T) {
	c := testConsole(t)
	body := coverJPEG(t)
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var active, peak atomic.Int32
	c.client.Transport = catalogTransport(func(r *http.Request) (*http.Response, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	var wg sync.WaitGroup
	for id := 1; id <= 8; id++ {
		wg.Go(func() {
			if w := coverRequest(c, "GET", fmt.Sprintf("id=%d", id), c.token); w.Code != 200 {
				t.Error(w.Code, w.Body.String())
			}
		})
	}
	for range 4 {
		<-entered
	}
	if active.Load() != 4 {
		t.Error("expected four concurrent transfers", active.Load())
	}
	close(release)
	wg.Wait()
	if peak.Load() != 4 {
		t.Fatal("concurrency limit", peak.Load())
	}
}
