package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
)

type rangeTerminalReader struct {
	data []byte
	err  error
}

func (r *rangeTerminalReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, r.err
	}
	return n, nil
}

func TestRangeBufferBoundaries(t *testing.T) {
	// Reuse dirty storage across responses, including a short final block.
	buf := bytes.Repeat([]byte{0xff}, 33)
	for _, tc := range []struct {
		name string
		size int
		data string
		err  error
	}{
		{"exact", 8, "abcdefgh", io.EOF},
		{"short", 8, "abc", io.EOF},
		{"empty", 8, "", io.EOF},
		{"oversized", 8, "abcdefghijkl", io.EOF},
		{"final-block", 3, "xyz", io.EOF},
		{"error-with-full-data", 8, "abcdefgh", io.ErrUnexpectedEOF},
		{"canceled", 8, "abc", context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &rangeTerminalReader{[]byte(tc.data), tc.err}
			got, err := readRangeBlock(r, buf[:tc.size+1])
			want := tc.data[:min(len(tc.data), tc.size+1)]
			wantErr := tc.err
			if wantErr == io.EOF || len(tc.data) > tc.size+1 {
				wantErr = nil
			}
			if string(got) != want || !errors.Is(err, wantErr) {
				t.Fatalf("got %q, %v; want %q, %v", got, err, want, wantErr)
			}
		})
	}
}

func TestRangeBufferRetryAndPartialFinalBlock(t *testing.T) {
	body := make([]byte, 2*rangeBlockSize+137)
	for i := range body {
		body[i] = byte((i/131071 + i) % 251)
	}
	var mu sync.Mutex
	attempts := make(map[int64]int)
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		attempts[start]++
		attempt := attempts[start]
		mu.Unlock()
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.WriteHeader(206)
		// Unknown body length exercises payload validation rather than the
		// Content-Length check. The retry must overwrite these stale bytes.
		w.(http.Flusher).Flush()
		if start == rangeBlockSize && attempt == 1 {
			w.Write(bytes.Repeat([]byte{0xff}, 257))
			return
		}
		w.Write(body[start : end+1])
	})
	target := videoURL(string(body))
	if _, err := s.Prefetch(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest("GET", target, nil)
	v, err := s.parseResolved(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(s.cfg.videoFile(v.key))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("published bytes differ: size=%d err=%v", len(got), err)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts[rangeBlockSize] != 2 {
		t.Fatalf("middle block attempts=%d, want 2", attempts[rangeBlockSize])
	}
}

func BenchmarkRangeBlockBuffer(b *testing.B) {
	payload := bytes.Repeat([]byte{'x'}, int(rangeBlockSize))
	b.Run("ReadAll", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(rangeBlockSize)
		for b.Loop() {
			data, err := io.ReadAll(io.LimitReader(bytes.NewReader(payload), rangeBlockSize+1))
			if err != nil || len(data) != len(payload) {
				b.Fatal(len(data), err)
			}
		}
	})
	b.Run("WorkerBuffer", func(b *testing.B) {
		buf := make([]byte, rangeBlockSize+1)
		b.ReportAllocs()
		b.SetBytes(rangeBlockSize)
		for b.Loop() {
			data, err := readRangeBlock(bytes.NewReader(payload), buf)
			if err != nil || len(data) != len(payload) {
				b.Fatal(len(data), err)
			}
		}
	})
}
