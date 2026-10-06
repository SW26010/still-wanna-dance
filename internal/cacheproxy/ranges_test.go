package cacheproxy

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestRangeTailReadableWhilePrefixBlocked(t *testing.T) {
	body := bytes.Repeat([]byte("01234567"), int(rangeBlockSize*3/8))
	for i := range body {
		body[i] = byte((i/131071 + i) % 251)
	}
	prefix, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	counts := make(map[int64]int)
	active, peak := 0, 0
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		counts[start]++
		active++
		peak = max(peak, active)
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.Header().Set("ETag", `"stable"`)
		w.WriteHeader(206)
		w.(http.Flusher).Flush()
		if start == 0 {
			close(prefix)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		} else if r.Header.Get("If-Match") != `"stable"` {
			t.Error("missing entity condition")
		}
		w.Write(body[start : end+1])
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	target := fmt.Sprintf("http://play.udon.dance/files/1/42-video.mp4?e=%x&s=%d", md5.Sum(body), len(body))
	req, _ := http.NewRequest("GET", target, nil)
	v, err := s.parseResolved(req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f, first, err := s.obtainMode(ctx, v, true)
	if err != nil {
		t.Fatal(err)
	}
	if first != nil {
		defer first.Close()
	}
	<-prefix
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reader := f.spool.reader(ctx, int64(len(body)))
			defer reader.Close()
			reader.Seek(-128, io.SeekEnd)
			got, err := io.ReadAll(reader)
			if err != nil || !bytes.Equal(got, body[len(body)-128:]) {
				t.Errorf("tail: %d bytes, %v", len(got), err)
			}
		}()
	}
	wg.Wait()
	select {
	case <-f.done:
		t.Fatal("completed through a hole")
	default:
	}
	if _, err := os.Stat(s.cfg.videoFile(v.key)); !os.IsNotExist(err) {
		t.Fatal("published incomplete media", err)
	}
	close(release)
	select {
	case <-f.done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if f.err != nil {
		t.Fatal(f.err)
	}
	got, err := os.ReadFile(f.path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatal("published bytes differ", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if peak > 2 || len(counts) != 3 {
		t.Fatal("unbounded requests", peak, counts)
	}
	for _, n := range counts {
		if n != 1 {
			t.Fatal("overlapping readers duplicated requests", counts)
		}
	}
}

func TestRangeFailuresDoNotPublish(t *testing.T) {
	for _, failure := range []string{"wrong-range", "wrong-size", "short", "changed-entity", "ignored", "checksum"} {
		t.Run(failure, func(t *testing.T) {
			body := bytes.Repeat([]byte("x"), int(rangeBlockSize+10))
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				var start, end int64
				fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
				total := len(body)
				if failure == "wrong-size" {
					total++
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
				w.Header().Set("ETag", `"one"`)
				if start > 0 {
					switch failure {
					case "wrong-range":
						w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", end, total))
					case "changed-entity":
						w.Header().Set("ETag", `"two"`)
					case "ignored":
						w.WriteHeader(200)
						w.Write(body)
						return
					case "short":
						end--
					}
				}
				w.WriteHeader(206)
				w.Write(body[start : end+1])
			})
			key := fmt.Sprintf("%x", md5.Sum(body))
			if failure == "checksum" {
				key = "00000000000000000000000000000000"
			}
			target := fmt.Sprintf("http://play.udon.dance/files/1/42-video.mp4?e=%s&s=%d", key, len(body))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err := s.Prefetch(ctx, target); err == nil {
				t.Fatal("accepted bad range")
			}
			if _, err := os.Stat(s.cfg.videoFile(key)); !os.IsNotExist(err) {
				t.Fatal("published failed download", err)
			}
		})
	}
}

func TestRangeHTTPClientTail(t *testing.T) {
	// Cover the HTTP range parser as well as direct spool readers.
	body := []byte("a complete ranged response")
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "video", time.Time{}, bytes.NewReader(body))
	})
	target := fmt.Sprintf("http://play.udon.dance/files/1/42-video.mp4?e=%x&s=%d", md5.Sum(body), len(body))
	r := httptest.NewRequest("GET", target, nil)
	r.Header.Set("Range", "bytes=-8")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != string(body[len(body)-8:]) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRangeCancellationWakesBlockedReaders(t *testing.T) {
	prefix := make(chan struct{})
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", rangeBlockSize-1, rangeBlockSize*2))
		w.WriteHeader(206)
		w.(http.Flusher).Flush()
		close(prefix)
		<-r.Context().Done()
	})
	target := fmt.Sprintf("http://play.udon.dance/files/1/42-video.mp4?e=00000000000000000000000000000000&s=%d", rangeBlockSize*2)
	r, _ := http.NewRequest("GET", target, nil)
	v, _ := s.parseResolved(r)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f, reader, err := s.obtain(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	<-prefix
	// Sharing must reject inconsistent size without touching the active task.
	other := v
	other.size++
	if _, _, err := s.obtain(ctx, other); err == nil {
		t.Fatal("shared inconsistent size")
	}
	s.cancel()
	if _, err := reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("canceled read succeeded")
	}
	select {
	case <-f.done:
	case <-ctx.Done():
		t.Fatal("range workers survived cancellation")
	}
}

func TestRangeReadersSeekBackAndForth(t *testing.T) {
	body := make([]byte, rangeBlockSize*5+137)
	for i := range body {
		body[i] = byte((i/65521 + i) % 251)
	}
	var mu sync.Mutex
	counts := make(map[int64]int)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		mu.Lock()
		counts[start]++
		mu.Unlock()
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.WriteHeader(206)
		w.(http.Flusher).Flush()
		if start == 0 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Write(body[start : end+1])
	})
	target := fmt.Sprintf("http://play.udon.dance/files/1/42-video.mp4?e=%x&s=%d", md5.Sum(body), len(body))
	r, _ := http.NewRequest("GET", target, nil)
	v, _ := s.parseResolved(r)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f, reader, err := s.obtain(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	if reader != nil {
		defer reader.Close()
	}
	unblock()
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.spool.reader(ctx, int64(len(body)))
			defer r.Close()
			for _, offset := range []int64{4*rangeBlockSize + 19, rangeBlockSize - 37, 3*rangeBlockSize + 1, 0, 5 * rangeBlockSize, 2*rangeBlockSize - 43, 511} {
				if _, err := r.Seek(offset, io.SeekStart); err != nil {
					t.Error(err)
					return
				}
				n := min(int64(113), int64(len(body))-offset)
				got := make([]byte, n)
				if _, err := io.ReadFull(r, got); err != nil || !bytes.Equal(got, body[offset:offset+n]) {
					t.Errorf("offset %d: %v", offset, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	select {
	case <-f.done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if f.err != nil {
		t.Fatal(f.err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(counts) != 6 {
		t.Fatal(counts)
	}
	for _, n := range counts {
		if n != 1 {
			t.Fatal("duplicate block request", counts)
		}
	}
}
