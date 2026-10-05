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
	c.client.Transport = catalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.udon.dance/Api/Songs/list" {
			t.Fatalf("unexpected catalog URL: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"time":"20261004235822","groups":{"contents":[{"songInfos":[{"id":1}]}]}}`))}, nil
	})
	if _, err := c.catalog(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUdonCatalogPreservesAndValidatesTopLevelTime(t *testing.T) {
	for _, value := range []string{"20261004235822", "", "20260230000000", "now"} {
		body := fmt.Sprintf(`{"time":%q,"groups":{"contents":[{"songInfos":[{"id":1,"name":"one"}]}]}}`, value)
		catalog, err := parseCatalog(strings.NewReader(body))
		if value == "20261004235822" {
			if err != nil || catalog.Revision != value || len(catalog.Songs) != 1 {
				t.Fatal(catalog, err)
			}
		} else if err == nil {
			t.Fatal("invalid Udon time accepted", value)
		}
	}
}

func TestCatalogSlowBodyHasIndependentTimeout(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"time":"20261004235822","groups":`)
		w.(http.Flusher).Flush()
		time.Sleep(60 * time.Millisecond)
		fmt.Fprint(w, `{"contents":[{"songInfos":[{"id":1,"name":"song"}]}]}}`)
	}))
	defer s.Close()
	c := testConsole(t)
	c.apiBase = s.URL
	c.client = s.Client()
	c.client.Timeout = 20 * time.Millisecond
	catalog, err := c.catalog(context.Background())
	if err != nil || len(catalog.Songs) != 1 {
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
		fmt.Fprint(w, `{"time":"20261004235822","groups":`)
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	}))
	defer s.Close()
	c := testConsole(t)
	c.apiBase = s.URL
	c.client = s.Client()
	_, err := c.catalog(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

func TestCatalogTruncatedBodyIsNotFormatError(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		fmt.Fprint(w, `{"time":"20261004235822","groups":`)
	}))
	defer s.Close()
	c := testConsole(t)
	c.apiBase = s.URL
	c.client = s.Client()
	_, err := c.catalog(context.Background())
	if err == nil || !strings.Contains(err.Error(), "读取失败") || strings.Contains(err.Error(), "格式错误") {
		t.Fatalf("%v", err)
	}
}

func TestLiveCatalogCompleteBody(t *testing.T) {
	if os.Getenv("STEPSTASH_LIVE_CATALOG") != "1" {
		t.Skip("opt-in live catalog request")
	}
	c := testConsole(t)
	started := time.Now()
	catalog, err := c.catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("catalog songs=%d elapsed=%s", len(catalog.Songs), time.Since(started))
}
