package console

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type catalogTransport func(*http.Request) (*http.Response, error)

func (f catalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func (f catalogTransport) Changed() <-chan struct{}                          { return nil }
func (f catalogTransport) Candidates(_ context.Context, target string) ([]upstreamrequest.Candidate, error) {
	return f.Current(target), nil
}
func (f catalogTransport) Current(target string) []upstreamrequest.Candidate {
	u, _ := url.Parse(target)
	return []upstreamrequest.Candidate{{ID: "direct/" + u.Hostname() + "/192.0.2.1", Host: u.Hostname(), Mode: "direct", IP: "192.0.2.1", Transport: f}}
}

func TestBackupCatalogUsesHTTPS(t *testing.T) {
	c := testConsole(t)
	c.checksumURL = "https://x.kiva.moe/api/v2/wanna/songs"
	c.client.Transport = catalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://x.kiva.moe/api/v2/wanna/songs" {
			t.Fatalf("unexpected catalog URL: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(testMD5CatalogBody("20261004235822", map[int]string{1: "body"})))}, nil
	})
	if _, err := c.fetchCatalogSnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type truncatedCatalogBody struct{}

func (truncatedCatalogBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (truncatedCatalogBody) Close() error             { return nil }

func TestBackupCatalogTruncatedBodyDoesNotDowngrade(t *testing.T) {
	var schemes []string
	c := &Console{checksumURL: "https://x.kiva.moe/api/v2/wanna/songs"}
	c.client = &http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
		schemes = append(schemes, r.URL.Scheme)
		body := io.ReadCloser(io.NopCloser(strings.NewReader(testMD5CatalogBody("20261004235822", map[int]string{1: "body"}))))
		if r.URL.Scheme == "https" {
			body = truncatedCatalogBody{}
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body, Request: r}, nil
	})}
	catalog, err := c.fetchCatalogSnapshot(context.Background())
	if !errors.Is(err, io.ErrUnexpectedEOF) || len(catalog.Songs) != 0 || len(schemes) != 1 || schemes[0] != "https" {
		t.Fatal(catalog, schemes, err)
	}
}

func TestMD5CatalogBodyRespectsTimeout(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":200,`)
		w.(http.Flusher).Flush()
		time.Sleep(60 * time.Millisecond)
		fmt.Fprint(w, strings.TrimPrefix(testMD5CatalogBody("20261004235822", map[int]string{1: "body"}), `{"code":200,`))
	}))
	defer s.Close()
	c := testConsole(t)
	c.checksumURL = s.URL
	c.client = s.Client()
	c.client.Timeout = 20 * time.Millisecond
	catalog, err := c.fetchCatalogSnapshot(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || len(catalog.Songs) != 0 {
		t.Fatalf("songs=%v error=%v", catalog.Songs, err)
	}
	if c.client.Timeout != 20*time.Millisecond {
		t.Fatal("changed shared timeout")
	}
}

func TestCatalogBodyCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":200,`)
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	}))
	defer s.Close()
	c := testConsole(t)
	c.checksumURL = s.URL
	c.client = s.Client()
	_, err := c.fetchCatalogSnapshot(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

func TestCatalogTruncatedBodyIsNotFormatError(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		fmt.Fprint(w, `{"code":200,`)
	}))
	defer s.Close()
	c := testConsole(t)
	c.checksumURL = s.URL
	c.client = s.Client()
	_, err := c.fetchCatalogSnapshot(context.Background())
	if err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("%v", err)
	}
}

func TestLiveCatalogCompleteBody(t *testing.T) {
	if os.Getenv("SWD_LIVE_CATALOG") != "1" {
		t.Skip("opt-in live catalog request")
	}
	c := testConsole(t)
	c.checksumURL = "https://x.kiva.moe/api/v2/wanna/songs"
	started := time.Now()
	catalog, err := c.fetchCatalogSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("catalog songs=%d elapsed=%s", len(catalog.Songs), time.Since(started))
}
