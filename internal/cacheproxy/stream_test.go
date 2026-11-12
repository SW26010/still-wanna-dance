package cacheproxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestColdStreamStartsBeforeCompletionAndSharesSeekingReaders(t *testing.T) {
	body := strings.Repeat("0123456789abcdef", int(2*rangeBlockSize/16))
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var count atomic.Int32
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		var start, end int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		if start > 0 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.WriteHeader(206)
		io.WriteString(w, body[start:end+1])
	})
	var handlers sync.WaitGroup
	tailStarted := make(chan struct{})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		if r.Header.Get("Range") == "bytes=1048576-" {
			close(tailStarted)
		}
		s.ServeHTTP(w, r)
	}))
	defer local.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	get := func(rangeValue string) *http.Response {
		t.Helper()
		r, _ := http.NewRequest("GET", local.URL+strings.TrimPrefix(videoURL(body), "http://play.udon.dance"), nil)
		r.Host = "play.udon.dance"
		if rangeValue != "" {
			r.Header.Set("Range", rangeValue)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := get("")
	defer resp.Body.Close()
	first := make([]byte, 512)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatal("prefix blocked until download completion", err)
	}
	if string(first) != body[:512] {
		t.Fatal("bad first bytes")
	}
	rangeResp := get("bytes=1024-1535")
	rangeBody, err := io.ReadAll(rangeResp.Body)
	rangeResp.Body.Close()
	if err != nil || rangeResp.StatusCode != 206 || string(rangeBody) != body[1024:1536] {
		t.Fatal("independent range reader failed", err)
	}
	if _, err := os.Stat(testVideoFile(t, cfg, payload)); !os.IsNotExist(err) {
		t.Fatal("partial video published")
	}
	// A resolver dropping its connection must not cancel the shared download.
	resp.Body.Close()
	// Missing bytes now delay the response headers too. Start the request
	// independently, then release the missing range response.
	var remaining *http.Response
	var tailErr error
	tailDone := make(chan struct{})
	go func() {
		defer close(tailDone)
		r, _ := http.NewRequest("GET", local.URL+strings.TrimPrefix(videoURL(body), "http://play.udon.dance"), nil)
		r.Host = "play.udon.dance"
		r.Header.Set("Range", "bytes=1048576-")
		remaining, tailErr = client.Do(r)
	}()
	select {
	case <-tailStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("tail request never arrived")
	}
	unblock()
	<-tailDone
	if tailErr != nil {
		t.Fatal(tailErr)
	}
	defer remaining.Body.Close()
	tail, err := io.ReadAll(remaining.Body)
	if err != nil || string(tail) != body[rangeBlockSize:] {
		t.Fatal("tail failed after resolver disconnect", err)
	}
	s.wg.Wait()
	handlers.Wait() // Receiving the last byte can precede the handler's deferred spool release.
	if count.Load() != 2 {
		t.Fatal("duplicate downloads", count.Load())
	}
	assertNoPartial(t, cfg.tempDir())
	assertResponse(t, request(s, "GET", videoURL(body), nil), 200, body)
}

func TestDeliveredCorruptStreamNeverPublishes(t *testing.T) {
	body := strings.Repeat("a", int(2*rangeBlockSize))
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		if start > 0 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.WriteHeader(206)
		if start == 0 {
			io.WriteString(w, body[start:end+1])
		} else {
			io.WriteString(w, strings.Repeat("b", int(end-start+1)))
		}

	})
	local := httptest.NewServer(s)
	defer local.Close()
	r, _ := http.NewRequest("GET", local.URL+strings.TrimPrefix(videoURL(body), "http://play.udon.dance"), nil)
	r.Host = "play.udon.dance"
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, 1024)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatal(err)
	}
	unblock()
	rest, err := io.ReadAll(resp.Body)
	if err != nil || len(first)+len(rest) != len(body) {
		t.Fatal("byte delivery depended on whole-video checksum", len(rest), err)
	}
	s.wg.Wait()
	for _, dir := range []string{cfg.videosDir(), cfg.tempDir()} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.mp4"))
		if len(files) != 0 {
			t.Fatal("corrupt cache published", files)
		}
	}
	assertNoPartial(t, cfg.tempDir())
	if !bytes.Equal(first, []byte(body[:1024])) {
		t.Fatal("unexpected prefix")
	}
}

func TestLargeStreamPayloadRemainsExact(t *testing.T) {
	body := strings.Repeat("0123456789abcdef", 512*1024)
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		serveFixtureContent(w, r, body)
	})
	started := time.Now()
	assertResponse(t, request(s, "GET", videoURL(body), nil), 200, body)
	t.Log("8 MiB stream duration:", time.Since(started))
}
