package console

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type catalogTransport func(*http.Request) (*http.Response, error)

func (f catalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCatalogUsesHTTPSForProductionAPI(t *testing.T) {
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
	if os.Getenv("STEPSTASH_LIVE_CATALOG") != "1" {
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
