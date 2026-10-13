package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

func fixtureBody() ([]byte, []string) {
	body := make([]byte, 4*blockSize)
	var hashes []string
	for i := range 4 {
		block := body[int64(i)*blockSize : int64(i+1)*blockSize]
		for j := range block {
			block[j] = byte(i*37 + j%251)
		}
		hash := sha256.Sum256(block)
		hashes = append(hashes, hex.EncodeToString(hash[:]))
	}
	return body, hashes
}

func TestMeasureConcurrentRangesAndLongControl(t *testing.T) {
	body, hashes := fixtureBody()
	for _, n := range []int{0, 1, 2, 3, 4} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			var mu sync.Mutex
			arrived := 0
			gate := make(chan struct{})
			seen := make(map[int64]int)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var start, end int64
				if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				seen[start]++
				arrived++
				if arrived == max(n, 1) {
					close(gate)
				}
				mu.Unlock()
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
				w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
				w.Header().Set("ETag", `"fixed"`)
				w.WriteHeader(206)
				w.Write(body[start : end+1])
			}))
			defer server.Close()
			u, _ := url.Parse(server.URL)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := measure(ctx, target{URL: server.URL, Host: u.Hostname(), IP: u.Hostname(), Size: int64(len(body)), ETag: `"fixed"`}, int64(len(body)), n, hashes, time.Now())
			if !result.OK || result.Bytes != int64(len(body)) || result.Peak != max(n, 1) {
				t.Fatalf("invalid measurement: %+v", result)
			}
			if result.MiBPerSec <= 0 || result.MeanActive <= 0 || result.MeanActive > float64(max(n, 1))+0.01 {
				t.Fatalf("invalid metrics: %+v", result)
			}
			want := 4
			if n == 0 {
				want = 1
			}
			if len(result.Requests) != want || len(seen) != want {
				t.Fatal("missing/duplicate ranges")
			}
			for _, calls := range seen {
				if calls != 1 {
					t.Fatal("duplicate block")
				}
			}
			var actual []string
			for _, request := range result.Requests {
				actual = append(actual, request.Hashes...)
			}
			if fmt.Sprint(actual) != fmt.Sprint(hashes) {
				t.Fatal("incorrect content order")
			}
		})
	}
}

func TestMeasureRejectsInvalidData(t *testing.T) {
	body, hashes := fixtureBody()
	for _, bad := range []string{"status", "range", "length", "etag", "encoding", "corrupt", "short", "oversized"} {
		t.Run(bad, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data := bytes.Clone(body[:blockSize])
				w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", blockSize-1, len(body)))
				w.Header().Set("ETag", `"fixed"`)
				status := 206
				switch bad {
				case "status":
					status = 200
				case "range":
					w.Header().Set("Content-Range", "bytes 1-2/3")
				case "length":
					w.Header().Set("Content-Length", fmt.Sprint(blockSize-1))
					data = data[:blockSize-1]
				case "etag":
					w.Header().Set("ETag", `"changed"`)
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
				case "corrupt":
					data[123] ^= 1
				case "short":
					data = data[:blockSize-1]
				case "oversized":
					data = append(data, 0)
				}
				w.WriteHeader(status)
				w.(http.Flusher).Flush()
				w.Write(data)
			}))
			defer server.Close()
			u, _ := url.Parse(server.URL)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result := measure(ctx, target{URL: server.URL, Host: u.Hostname(), IP: u.Hostname(), Size: int64(len(body)), ETag: `"fixed"`}, blockSize, 1, hashes, time.Now())
			if result.OK || result.Error == "" {
				t.Fatal("invalid response counted as successful throughput")
			}
		})
	}
}
