package upstreamrequest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func readAttempt(ctx context.Context, client *http.Client, target string) (string, error) {
	r, _ := http.NewRequestWithContext(ctx, "GET", target, nil)
	resp, err := Do(client, r)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err == nil && string(b) != "valid" {
		err = errors.New("invalid content")
	}
	return string(b), err
}

func TestFetchProtocolPolicyWithRealTLSAndBodies(t *testing.T) {
	for _, mode := range []string{"https", "disconnect", "truncated", "timeout", "certificate", "status", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			var plaintext atomic.Int32
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				plaintext.Add(1)
				if r.Host != "example.com" || r.URL.RequestURI() != "/list?version=1" {
					t.Errorf("identity changed: %s %s", r.Host, r.URL)
				}
				io.WriteString(w, "valid")
			}))
			defer httpServer.Close()
			tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "disconnect":
					conn, _, _ := w.(http.Hijacker).Hijack()
					conn.Close()
				case "truncated":
					w.Header().Set("Content-Length", "100")
					io.WriteString(w, "partial")
				case "timeout":
					<-r.Context().Done()
				case "status":
					w.WriteHeader(500)
				case "invalid":
					io.WriteString(w, "wrong")
				default:
					io.WriteString(w, "valid")
				}
			}))
			defer tlsServer.Close()
			roots := x509.NewCertPool()
			if mode != "certificate" {
				roots.AddCert(tlsServer.Certificate())
			}
			tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				endpoint := tlsServer.Listener.Addr().String()
				if strings.HasSuffix(address, ":80") {
					endpoint = httpServer.Listener.Addr().String()
				}
				return (&net.Dialer{}).DialContext(ctx, network, endpoint)
			}}
			defer tr.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			limit := time.Second
			if mode == "timeout" {
				limit = 100 * time.Millisecond
			}
			value, err := Fetch(ctx, "https://example.com/list?version=1", []*http.Client{{Transport: tr}}, limit, readAttempt)
			fallback := mode == "disconnect" || mode == "truncated" || mode == "timeout"
			if (mode == "https" || fallback) && (err != nil || value != "valid") {
				t.Fatalf("value=%q err=%v", value, err)
			}
			if mode != "https" && !fallback && err == nil {
				t.Fatal("accepted failed secure request")
			}
			want := int32(0)
			if fallback {
				want = 1
			}
			if plaintext.Load() != want {
				t.Fatalf("plaintext requests=%d want=%d", plaintext.Load(), want)
			}
		})
	}
}

type attemptTransport func(*http.Request) (*http.Response, error)

func (f attemptTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchKeepsCandidateOrderAndSharedBudget(t *testing.T) {
	for _, winner := range []int{1, -1} {
		var calls []string
		var clients []*http.Client
		for i := range 10 {
			clients = append(clients, &http.Client{Transport: attemptTransport(func(r *http.Request) (*http.Response, error) {
				calls = append(calls, fmt.Sprintf("%s/%d", r.URL.Scheme, i))
				if i != winner {
					return nil, io.EOF
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("valid")), Header: make(http.Header), Request: r}, nil
			})})
		}
		_, err := Fetch(context.Background(), "https://example.com/list", clients, time.Second, readAttempt)
		if winner == 1 {
			if err != nil || !slices.Equal(calls, []string{"https/0", "https/1"}) {
				t.Fatal(calls, err)
			}
		} else if err == nil || !slices.Equal(calls, []string{"https/0", "https/1", "https/2", "https/3", "https/4", "http/0"}) {
			t.Fatal(calls, err)
		}
	}
}

func TestColdCandidatesInterleaveAddressFamilies(t *testing.T) {
	dns := &testDNS{addresses: []Address{
		{IP: "192.0.2.1", Expires: time.Now().Add(time.Minute)},
		{IP: "192.0.2.2", Expires: time.Now().Add(time.Minute)},
		{IP: "2001:db8::1", Expires: time.Now().Add(time.Minute)},
		{IP: "2001:db8::2", Expires: time.Now().Add(time.Minute)},
	}}
	pool, err := NewPool("direct", dns, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var got []string
	for _, c := range pool.Current("https://example.com/list") {
		got = append(got, c.IP)
	}
	if !slices.Equal(got, []string{"2001:db8::1", "192.0.2.1", "2001:db8::2", "192.0.2.2"}) {
		t.Fatal(got)
	}
}

func TestCatalogBodyKeepsDeadlineAfterHeaders(t *testing.T) {
	// Several unused candidates must not divide a healthy response's body
	// lifetime. This reproduced real Udon 200 responses being cut off repeatedly.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-time.After(150 * time.Millisecond):
			io.WriteString(w, "valid")
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	var clients []*http.Client
	for range 6 {
		clients = append(clients, server.Client())
	}
	value, err := Fetch(ctx, server.URL, clients, time.Second, readAttempt)
	if err != nil || value != "valid" {
		t.Fatal(value, err)
	}
}

func TestUpgradeConsumesAttemptBudgetAndStopsPlaintextLoop(t *testing.T) {
	var calls []string
	var clients []*http.Client
	for i := range 3 {
		clients = append(clients, &http.Client{Transport: attemptTransport(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, fmt.Sprintf("%s/%d", r.URL.Scheme, i))
			if r.URL.Scheme == "https" {
				return nil, io.EOF
			}
			return &http.Response{StatusCode: 301, Header: http.Header{"Location": {"https://example.com/list"}}, Body: http.NoBody, Request: r}, nil
		})})
	}
	_, err := Fetch(context.Background(), "https://example.com/list", clients, time.Second, readAttempt)
	if err == nil || !slices.Equal(calls, []string{"https/0", "https/1", "https/2", "http/0", "https/0"}) {
		t.Fatal(calls, err)
	}
}

func TestFetchDoesNotDowngradeLocalFailures(t *testing.T) {
	for _, failure := range []error{context.Canceled, ErrCapacity, ErrRetired, ErrUnavailable, x509.UnknownAuthorityError{}} {
		calls := 0
		client := &http.Client{Transport: attemptTransport(func(r *http.Request) (*http.Response, error) { calls++; return nil, failure })}
		_, err := Fetch(context.Background(), "https://example.com/list", []*http.Client{client}, time.Second, readAttempt)
		if err == nil || calls != 1 {
			t.Fatal(failure, calls, err)
		}
	}
}

func TestEndpointUpgradeDoesNotFollowPlaybackOrLoop(t *testing.T) {
	for _, mode := range []string{"upgrade", "video", "loop"} {
		var calls []string
		client := &http.Client{Transport: attemptTransport(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, r.URL.String())
			location := "https://example.com/play?id=42"
			if mode == "video" {
				location = "http://example.com/files/video.mp4"
			}
			if r.URL.Scheme == "https" {
				if mode == "upgrade" {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("valid")), Request: r}, nil
				}
				location = "http://example.com/play?id=42"
			}
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {location}}, Body: http.NoBody, Request: r}, nil
		})}
		_, err := Fetch(context.Background(), "http://example.com/play?id=42", []*http.Client{client}, time.Second, readAttempt)
		if mode == "upgrade" && err != nil {
			t.Fatal(err)
		}
		want := 2
		if mode == "video" {
			want = 1
		}
		if len(calls) != want {
			t.Fatal(calls)
		}
	}
}
