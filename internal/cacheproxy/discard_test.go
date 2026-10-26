package cacheproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

type discardTestReader struct {
	t         *testing.T
	remaining int64
}

func (r *discardTestReader) Read(p []byte) (int, error) {
	if len(p) > 32<<10 {
		r.t.Errorf("media buffered in %d-byte blocks", len(p))
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), r.remaining)
	clear(p[:n])
	r.remaining -= n
	return int(n), nil
}

func TestDiscardLoaderStreamsWithoutFilesOrIntegrityCheck(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(file, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TMP", "TEMP", "TMPDIR"} {
		t.Setenv(name, file)
	}
	var mu sync.Mutex
	seen := map[string]int{}
	var received atomic.Int64
	var blocking atomic.Bool
	loader, err := NewDiscardLoader(DiscardConfig{
		ObserveBytes: func(n int64) { received.Add(n) },
		Observe: func(e blocksched.Event) {
			if e.Blocking {
				blocking.Store(true)
			}
		},
		Transport: resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			var start, end int64
			if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
				return nil, err
			}
			mu.Lock()
			seen[r.URL.String()+r.Header.Get("Range")]++
			mu.Unlock()
			return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, 32<<20)}},
				ContentLength: end - start + 1, Body: io.NopCloser(&discardTestReader{t, end - start + 1})}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer loader.Close()
	loader.SetSoftLimit(1)
	if loader.SetSoftLimit(17) {
		t.Fatal("diagnostic limit exceeded 16")
	}
	results := make(chan error, 2)
	for _, digest := range []string{"11111111111111111111111111111111", "22222222222222222222222222222222"} {
		target := "https://play.udon.dance/files/123/42-abc.mp4?e=" + digest + "&s=33554432"
		go func() { results <- loader.Load(context.Background(), target) }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	loader.Close()
	if received.Load() != 64<<20 || blocking.Load() {
		t.Fatalf("bytes=%d blocking=%v", received.Load(), blocking.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 64 {
		t.Fatal(len(seen))
	}
	for _, n := range seen {
		if n != 1 {
			t.Fatal("range repeated")
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("diagnostic created storage: %v %v", entries, err)
	}
	if loader.s.Store != nil || loader.s.rangeBuffers != nil {
		t.Fatal("diagnostic allocated media cache")
	}
}

func TestDiscardLoaderTwoVideosShareOneCeilingAndDrain(t *testing.T) {
	var active, peak atomic.Int32
	started := make(chan struct{}, 32)
	loader, err := NewDiscardLoader(DiscardConfig{Transport: resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
		n := active.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		started <- struct{}{}
		defer active.Add(-1)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer loader.Close()
	loader.SetSoftLimit(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 2)
	for _, digest := range []string{"11111111111111111111111111111111", "22222222222222222222222222222222"} {
		go func() {
			finished <- loader.Load(ctx, "https://play.udon.dance/files/123/42-abc.mp4?e="+digest+"&s=33554432")
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not start")
	}
	select {
	case <-started:
		t.Fatal("two videos escaped global single limit")
	case <-time.After(300 * time.Millisecond):
	}
	loader.SetSoftLimit(2)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("ceiling release did not admit second video")
	}
	cancel()
	loader.Close()
	for range 2 {
		if err := <-finished; err == nil {
			t.Fatal("canceled load succeeded")
		}
	}
	if active.Load() != 0 || peak.Load() != 2 {
		t.Fatalf("active=%d peak=%d", active.Load(), peak.Load())
	}
}
