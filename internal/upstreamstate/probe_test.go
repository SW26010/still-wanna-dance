package upstreamstate

import (
	"context"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type countingBody struct{ read atomic.Int64 }

func TestCatalogSamplesOnlyValidSongs(t *testing.T) {
	client := requestClient(transportFunc(func(*http.Request) (*http.Response, error) {
		return response(200, `{"groups":{"contents":[{"songInfos":[{"id":-1},{"id":0},{"id":42},{"id":42}]},{"songInfos":[{"id":73},{"id":99}]}]}}`), nil
	}))
	for range 64 {
		o, id := probeCatalog(context.Background(), client, DefaultPolicy())
		if o.state != "available" || (id != 42 && id != 73 && id != 99) {
			t.Fatalf("invalid song sample: %d, %+v", id, o)
		}
	}
}

func TestSampleSongMayResolveToDifferentResourceID(t *testing.T) {
	client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("id") != "42" {
			t.Fatal("wrong requested song", r.URL)
		}
		resp := response(http.StatusFound, "")
		resp.Header.Set("Location", strings.Replace(videoFixture, "/42-", "/43-", 1))
		return resp, nil
	}))
	o, sample := probePlayback(context.Background(), client, DefaultPolicy(), 42, videoRoutes[0])
	if o.state != "available" || o.songID != 42 || sample == nil || !strings.Contains(sample.url, "/43-") {
		t.Fatal("conflated requested song with resource ID", o, sample)
	}
}

func (b *countingBody) Read(p []byte) (int, error) { b.read.Add(int64(len(p))); return len(p), nil }
func (*countingBody) Close() error                 { return nil }
func TestResourceRejectsIgnoredRangeAndOversize(t *testing.T) {
	for _, status := range []int{200, 206} {
		body := &countingBody{}
		client := requestClient(transportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Range": []string{"bytes 0-131071/131072"}}, Body: body}, nil
		}))
		s, _ := parseSample(videoFixture)
		o := probeResource(context.Background(), client, DefaultPolicy(), 42, videoRoutes[0], s)
		if o.state == "available" {
			t.Fatal("invalid resource available")
		}
		want := int64(0)
		if status == 206 {
			want = s.size + 1
		}
		if body.read.Load() != want {
			t.Fatal("unbounded read", body.read.Load(), want)
		}
	}
}

func TestSamplesAndRedirectBoundaries(t *testing.T) {
	if _, err := parseSample(strings.Replace(videoFixture, "/42-", "/%34%32-", 1)); err == nil {
		t.Fatal("accepted encoded video path")
	}
	for _, u := range []string{"http://127.0.0.1/private", strings.Replace(videoFixture, "play.udon.dance", "evil.invalid", 1), videoFixture + "&s=12", strings.Replace(videoFixture, "131072", "0", 1), strings.Replace(videoFixture, "https://", "https://user:secret@", 1)} {
		if _, err := parseSample(u); err == nil {
			t.Fatal("unsafe sample", u)
		}
	}
	for _, target := range []string{"https://evil.invalid/", strings.Replace(videoFixture, "play.udon.dance", "nya.xin.moe", 1), strings.Replace(videoFixture, "131072", "262144", 1)} {
		var calls atomic.Int32
		client := requestClient(transportFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			r := response(302, "")
			r.Header.Set("Location", target)
			return r, nil
		}))
		s, _ := parseSample(videoFixture)
		o := probeResource(context.Background(), client, DefaultPolicy(), 42, videoRoutes[0], s)
		if o.state != "invalid" || calls.Load() != 1 || o.latency != 0 {
			t.Fatal("followed unsafe redirect", o, calls.Load())
		}
	}
}

type slowBody struct{ io.Reader }

func (b slowBody) Read(p []byte) (int, error) {
	time.Sleep(25 * time.Millisecond)
	return b.Reader.Read(p)
}
func (slowBody) Close() error { return nil }
func TestFirstByteLatencySeparateFromBodyAndInvalidCatalog(t *testing.T) {
	client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		time.Sleep(2 * time.Millisecond)
		httptrace.ContextClientTrace(r.Context()).GotFirstResponseByte()
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: slowBody{strings.NewReader(catalogFixture)}}, nil
	}))
	o, id := probeCatalog(context.Background(), client, DefaultPolicy())
	if id != 42 || o.state != "available" || o.duration <= o.latency+20*time.Millisecond {
		t.Fatal("latency includes body", o)
	}
	for _, body := range []string{"<html>challenge</html>", "{}", catalogFixture + "garbage"} {
		client := requestClient(transportFunc(func(*http.Request) (*http.Response, error) { return response(200, body), nil }))
		o, _ := probeCatalog(context.Background(), client, DefaultPolicy())
		if o.state != "invalid" {
			t.Fatal(o)
		}
	}
}

func TestWrongPlaybackHostAndNoRedirectFollowing(t *testing.T) {
	var calls atomic.Int32
	client := requestClient(transportFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		r := response(302, "")
		r.Header.Set("Location", strings.Replace(videoFixture, "play.udon.dance", "nya.xin.moe", 1))
		return r, nil
	}))
	o, s := probePlayback(context.Background(), client, DefaultPolicy(), 42, videoRoutes[0])
	if o.state != "invalid" || s != nil || calls.Load() != 1 {
		t.Fatal(o, s, calls.Load())
	}
}

func TestSmallResourceAndPermittedRedirect(t *testing.T) {
	var calls atomic.Int32
	client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.Header.Get("Range") != "bytes=0-3" {
			t.Error(r.URL, r.Header)
		}
		if calls.Add(1) == 1 {
			time.Sleep(25 * time.Millisecond)
			resp := response(302, "")
			resp.Header.Set("Location", strings.Replace(strings.Replace(videoFixture, "131072", "4", 1), "42-abc", "42-other", 1))
			return resp, nil
		}
		resp := response(206, "data")
		resp.Body = slowBody{strings.NewReader("data")}
		resp.Header.Set("Content-Range", "bytes 0-3/4")
		return resp, nil
	}))
	s, err := parseSample(strings.Replace(strings.Replace(videoFixture, "131072", "4", 1), "https://", "http://", 1))
	if err != nil {
		t.Fatal(err)
	}
	o := probeResource(context.Background(), client, DefaultPolicy(), 42, videoRoutes[0], s)
	if o.state != "available" || o.bytes != 4 || calls.Load() != 2 {
		t.Fatal(o, calls.Load())
	}
	if o.stage != "body" || o.latency < 25*time.Millisecond || o.duration-o.latency < 25*time.Millisecond {
		t.Fatal("resource timing must include redirects and separate body time", o)
	}
}
