//go:build windows && cf_block_experiment

package console

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCFTriadPlanHarness(t *testing.T) {
	p := cfTriadPools{Method: "cf-triad-v1", Pools: map[string][]string{}}
	for _, key := range []string{"A", "B_uniform", "B_stratified"} {
		for i := 0; i < 104; i++ {
			p.Pools[key] = append(p.Pools[key], fmt.Sprintf("104.16.%d.1", i))
		}
	}
	a, err := cfTriadPlan(p, []string{"2606:4700::1"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := cfTriadPlan(p, []string{"2606:4700::1"}, 2)
	strip := func(rows []cfTriadCandidate) []cfTriadCandidate {
		var out []cfTriadCandidate
		for _, r := range rows {
			if r.Pool != "C" {
				out = append(out, r)
			}
		}
		return out
	}
	x, y := strip(a), strip(b)
	slices.Reverse(y)
	if len(a) != 314 || !slices.Equal(x, y) {
		t.Fatal("unequal pools or holdout order")
	}
	counts := map[string]int{}
	for _, r := range a {
		counts[r.Pool]++
	}
	for _, key := range []string{"A", "B_uniform", "B_stratified"} {
		if counts[key] != 104 {
			t.Fatal("pool overlap must retain independent membership")
		}
	}
	for i := 0; i < 4; i++ {
		order := cfTriadOrder(i)
		h, j := slices.Index(order, "http"), slices.Index(order, "https")
		if h-j != 1 && j-h != 1 {
			t.Fatal("video pair not adjacent")
		}
	}
	p.Pools["A"][0] = "127.0.0.1"
	if _, err := cfTriadPlan(p, nil, 1); err == nil {
		t.Fatal("private candidate accepted")
	}
}

func TestCFTriadPilotBalanceHarness(t *testing.T) {
	for _, count := range []int{15, 16, 17} {
		for window := 1; window <= 2; window++ {
			orders := map[int][]bool{}
			for round := 0; round < 2; round++ {
				for i := 0; i < count; i++ {
					pos := (i + round*7) % count
					o := cfTriadPilotOrder(pos, round, window)
					orders[pos] = append(orders[pos], slices.Index(o, "http") < slices.Index(o, "https"))
				}
			}
			for _, o := range orders {
				if len(o) != 2 || o[0] == o[1] {
					t.Fatal("each IP must receive opposite protocol order across pilot rounds")
				}
			}
		}
	}
}

func TestCFTriadReadHarness(t *testing.T) {
	content := []byte(strings.Repeat("v", 2048))
	hash := fmt.Sprintf("%x", sha256.Sum256(content[:1024]))
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprintf("tls-%v", secure), func(t *testing.T) {
			for _, kind := range []string{"ok", "redirect", "status", "bad-range", "bad-etag", "bad-content", "truncated", "timeout"} {
				t.Run(kind, func(t *testing.T) {
					handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Host == "" || !strings.HasPrefix(r.Host, "example.com:") || r.Header.Get("Range") != "bytes=0-1023" || r.Header.Get("Accept-Encoding") != "identity" {
							t.Error("request semantics changed")
						}
						if kind == "redirect" {
							w.Header().Set("Location", "https://example.com/other")
							w.WriteHeader(302)
							return
						}
						if kind == "status" {
							w.WriteHeader(503)
							return
						}
						w.Header().Set("ETag", `"fixture"`)
						w.Header().Set("Content-Range", "bytes 0-1023/2048")
						w.Header().Set("Content-Length", "1024")
						if kind == "bad-range" {
							w.Header().Set("Content-Range", "bytes 1-1024/2048")
						}
						if kind == "bad-etag" {
							w.Header().Set("ETag", `"other"`)
						}
						w.WriteHeader(206)
						if kind == "timeout" {
							w.Write(content[:20])
							w.(http.Flusher).Flush()
							<-r.Context().Done()
							return
						}
						if kind == "truncated" {
							w.Write(content[:20])
							return
						}
						if kind == "bad-content" {
							w.Write([]byte(strings.Repeat("x", 1024)))
							return
						}
						w.Write(content[:1024])
					})
					var s *httptest.Server
					if secure {
						s = httptest.NewTLSServer(handler)
					} else {
						s = httptest.NewServer(handler)
					}
					defer s.Close()
					u, _ := url.Parse(s.URL)
					_, port, _ := net.SplitHostPort(u.Host)
					u.Host = net.JoinHostPort("example.com", port)
					tr := cfTriadTransport((&net.Dialer{}).DialContext, "127.0.0.1")
					defer tr.CloseIdleConnections()
					if secure {
						tr.TLSClientConfig = s.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
						tr.TLSClientConfig.InsecureSkipVerify = false
					}
					target := cfexpTarget{Song: 1, URL: u.String(), Size: 2048, ETag: `"fixture"`}
					r, body := cfTriadRead(context.Background(), tr, target, u.Scheme, "127.0.0.1", 1024, hash, 150*time.Millisecond)
					want := map[string]string{"ok": "", "redirect": "redirect", "status": "stop_status", "bad-range": "headers", "bad-etag": "headers", "bad-content": "content_mismatch", "truncated": "body_read", "timeout": "body_read"}[kind]
					if r.Error != want {
						t.Fatalf("want %q, got %+v", want, r)
					}
					if kind == "ok" && (r.Bytes != 1024 || len(body) != 1024 || r.Hash != hash || r.TLSVerified != secure || r.Reused || r.FirstByteMS <= 0 || r.ElapsedMS < r.HeadersMS) {
						t.Fatalf("missing valid content/timing: %+v", r)
					}
					if (kind == "truncated" || kind == "timeout") && r.Bytes != 20 {
						t.Fatal("partial delivery lost")
					}
				})
			}
		})
	}
}

func TestCFTriadRejectUntrustedTLSHarness(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted TLS reached handler") }))
	defer s.Close()
	u, _ := url.Parse(s.URL)
	tr := cfTriadTransport((&net.Dialer{}).DialContext, "")
	defer tr.CloseIdleConnections()
	r, _ := cfTriadRead(context.Background(), tr, cfexpTarget{URL: u.String()}, "https", "", 1024, "", time.Second)
	if r.Error != "transport" || r.TLSVerified {
		t.Fatalf("untrusted certificate accepted: %+v", r)
	}
}

type cfTriadRoundTripFunc func(*http.Request) (*http.Response, error)

func (f cfTriadRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type cfTriadReadFunc func([]byte) (int, error)

func (f cfTriadReadFunc) Read(p []byte) (int, error) { return f(p) }

func TestCFTriadLateTraceHarness(t *testing.T) {
	callbackDone := make(chan struct{})
	tr := cfTriadRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(req.Context())
		trace.ConnectStart("tcp", "unused:80")
		trace.GotFirstResponseByte()
		content := strings.NewReader("test")
		first := true
		reader := cfTriadReadFunc(func(p []byte) (int, error) {
			if first {
				first = false
				go func() {
					trace.ConnectDone("tcp", "unused:80", nil)
					close(callbackDone)
				}()
				select {
				case <-callbackDone:
				case <-time.After(time.Second):
					return 0, fmt.Errorf("late trace callback blocked during body read")
				}
			}
			return content.Read(p)
		})
		return &http.Response{StatusCode: 206, Proto: "HTTP/1.1", ContentLength: 4,
			Header: http.Header{"Content-Range": {"bytes 0-3/4"}, "Etag": {`"fixture"`}},
			Body:   io.NopCloser(reader)}, nil
	})
	r, _ := cfTriadRead(context.Background(), tr,
		cfexpTarget{URL: "http://example.com/video", Size: 4, ETag: `"fixture"`},
		"http", "", 4, "", 2*time.Second)
	<-callbackDone
	if r.Error != "" || r.Bytes != 4 || r.TCPMS != 0 {
		t.Fatalf("late callback must finish independently and not change the header snapshot: %+v", r)
	}
}
