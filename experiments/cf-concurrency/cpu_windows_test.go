//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCPUWorkerRepeatedRangesAndCorruption(t *testing.T) {
	body, hashes := fixtureBody()
	reference := append(append(append([]string{}, hashes...), hashes...), hashes...)
	for _, bad := range []bool{false, true} {
		t.Run(fmt.Sprint(bad), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var start, end int64
				fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
				time.Sleep(15 * time.Millisecond)
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, 96*blockSize))
				w.Header().Set("Content-Length", fmt.Sprint(blockSize))
				w.Header().Set("ETag", `"fixed"`)
				w.WriteHeader(206)
				offset := start % (32 * blockSize)
				if bad {
					offset = (offset + blockSize) % (32 * blockSize)
				}
				w.Write(body[offset : offset+blockSize])
			}))
			defer server.Close()
			u, _ := url.Parse(server.URL)
			dir := t.TempDir()
			ref := filepath.Join(dir, "reference.json")
			out := filepath.Join(dir, "result.json")
			save(ref, reference)
			err := cpuTrial(context.Background(), target{URL: server.URL, Host: u.Hostname(), IP: u.Hostname(), Size: 96 * blockSize, ETag: `"fixed"`}, "burst", 4, time.Second, "test", ref, out)
			if (err != nil) != bad {
				t.Fatalf("error=%v corruption=%v", err, bad)
			}
			b, e := os.ReadFile(out)
			if e != nil {
				t.Fatal(e)
			}
			var r cpuReport
			if e = json.Unmarshal(b, &r); e != nil {
				t.Fatal(e)
			}
			if !bad && (r.Peak != 4 || len(r.Requests) < 8 || len(r.Samples) < 5 || len(r.CoreMasks) == 0) {
				t.Fatalf("missing measurement: peak=%d requests=%d samples=%d", r.Peak, len(r.Requests), len(r.Samples))
			}
		})
	}
}
