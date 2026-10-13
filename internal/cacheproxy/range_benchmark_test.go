package cacheproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// This benchmark also runs unchanged against the pre-#20 engine. Compare a
// per-request bottleneck (20 ms headers, 64 KiB/2 ms) with a shared link of
// 64 KiB/2 ms and no added header delay. Each iteration gets a fresh cache;
// no public network or game is involved.
func BenchmarkRangeDownload(b *testing.B) {
	for _, shared := range []bool{false, true} {
		link := "per_request"
		if shared {
			link = "shared_capacity"
		}
		for _, mode := range []string{"prefetch", "playback_seek"} {
			b.Run(link+"/"+mode, func(b *testing.B) {
				body := bytes.Repeat([]byte("benchmark"), int(12*rangeBlockSize/9)+13)
				linkTicker := time.NewTicker(2 * time.Millisecond)
				defer linkTicker.Stop()
				var mu sync.Mutex
				active, peak, requests, transferred := 0, 0, 0, int64(0)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					active++
					peak = max(peak, active)
					requests++
					mu.Unlock()
					defer func() { mu.Lock(); active--; mu.Unlock() }()
					var start, end int64
					fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
					if !shared {
						select {
						case <-time.After(20 * time.Millisecond):
						case <-r.Context().Done():
							return
						}
					}
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
					w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
					w.Header().Set("ETag", `"fixed-benchmark"`)
					w.WriteHeader(206)
					w.(http.Flusher).Flush()
					ticker := time.NewTicker(2 * time.Millisecond)
					defer ticker.Stop()
					ticks := ticker.C
					if shared {
						ticks = linkTicker.C
					}
					for offset := start; offset <= end; {
						select {
						case <-ticks:
						case <-r.Context().Done():
							return
						}
						to := min(offset+64*1024, end+1)
						n, err := w.Write(body[offset:to])
						mu.Lock()
						transferred += int64(n)
						mu.Unlock()
						if err != nil {
							return
						}
						offset = to
						w.(http.Flusher).Flush()
					}
				}))
				defer upstream.Close()
				var firstByte, seekWait time.Duration
				b.SetBytes(int64(len(body)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					cfg := fixtureConfig()
					cfg.OriginScheme, cfg.StorageDir = "http", b.TempDir()
					cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
					for host := range cfg.Origins {
						cfg.Origins[host] = strings.TrimPrefix(upstream.URL, "http://")
					}
					s, err := New(cfg)
					if err != nil {
						b.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					target := videoURL(string(body))
					b.StartTimer()
					if mode == "prefetch" {
						_, err = s.Prefetch(ctx, target)
					} else {
						start := time.Now()
						r, _ := http.NewRequest("GET", target, nil)
						v, _ := s.parseResolved(r)
						var f *flight
						var reader *spoolReader
						f, reader, err = s.obtain(ctx, v)
						if err == nil {
							buf := make([]byte, 1)
							_, err = io.ReadFull(reader, buf)
							firstByte += time.Since(start)
							if err == nil {
								start = time.Now()
								reader.Seek(10*rangeBlockSize, io.SeekStart)
								_, err = io.ReadFull(reader, buf)
								seekWait += time.Since(start)
							}
							reader.Close()
							select {
							case <-f.done:
								if err == nil {
									err = f.err
								}
							case <-ctx.Done():
								err = ctx.Err()
							}
						}
					}
					b.StopTimer()
					cancel()
					s.Close()
					if err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
				}
				b.StopTimer()
				mu.Lock()
				b.ReportMetric(float64(peak), "peak_requests")
				b.ReportMetric(float64(requests)/float64(b.N), "requests/op")
				b.ReportMetric(float64(transferred)/float64(b.N*len(body)), "traffic_ratio")
				mu.Unlock()
				if mode == "playback_seek" {
					b.ReportMetric(float64(firstByte.Microseconds())/float64(b.N)/1000, "first_body_ms")
					b.ReportMetric(float64(seekWait.Microseconds())/float64(b.N)/1000, "seek_ms")
				}
			})
		}
	}
}
