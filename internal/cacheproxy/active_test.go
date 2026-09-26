package cacheproxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestActiveSharedLifecycle(t *testing.T) {
	for _, outcome := range []string{"complete", "redirect", "fail", "close"} {
		t.Run(outcome, func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var calls atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if outcome == "redirect" && r.Host == "play.udon.dance" {
					http.Redirect(w, r, strings.Replace(videoURL(payload), "play.udon.dance", "nya.xin.moe", 1), http.StatusTemporaryRedirect)
					return
				}
				calls.Add(1)
				io.WriteString(w, payload[:5])
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				if outcome == "complete" || outcome == "redirect" {
					io.WriteString(w, payload[5:])
				}
			})
			v := parsedVideo(t, s, payload)
			first, reader, err := s.obtain(context.Background(), v)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			unknown, err := s.ActiveDownloads(context.Background())
			if err != nil || len(unknown.Tasks) != 1 || len(unknown.Tasks[0].Songs) != 0 {
				t.Fatalf("unknown song: %+v %v", unknown, err)
			}
			v.songID = "138"
			_, reader3, err := s.obtain(context.Background(), v)
			if err != nil {
				t.Fatal(err)
			}
			defer reader3.Close()
			v.songID = "140"
			second, reader2, err := s.obtainMode(context.Background(), v, true)
			if err != nil {
				t.Fatal(err)
			}
			defer reader2.Close()
			if first != second {
				t.Fatal("shared callers created separate flights")
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				snap, err := s.ActiveDownloads(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if len(snap.Tasks) != 1 {
					t.Fatalf("duplicate or missing flight: %+v", snap)
				}
				task := snap.Tasks[0]
				if task.Bytes == 5 && task.BytesPerSecond > 0 {
					host := "play.udon.dance"
					if outcome == "redirect" {
						host = "nya.xin.moe"
					}
					if task.Size != int64(len(payload)) || task.Host != host || len(task.Songs) != 2 || task.BytesPerSecond <= 0 || snap.BytesPerSecond != task.BytesPerSecond {
						t.Fatalf("bad snapshot: %+v", snap)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("no bytes observed")
				}
				time.Sleep(time.Millisecond)
			}
			assertResponse(t, request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=0-2"}), 206, payload[:3])
			assertResponse(t, request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=1-4"}), 206, payload[1:5])
			shared, err := s.ActiveDownloads(context.Background())
			if err != nil || len(shared.Tasks) != 1 || shared.Tasks[0].Bytes != 5 {
				t.Fatalf("Range bytes counted as upstream: %+v %v", shared, err)
			}
			var readers sync.WaitGroup
			for i := 0; i < 8; i++ {
				readers.Add(1)
				go func() {
					defer readers.Done()
					for j := 0; j < 20; j++ {
						if _, err := s.ActiveDownloads(context.Background()); err != nil {
							t.Error(err)
						}
					}
				}()
			}
			if outcome != "close" {
				unblock()
			}
			readers.Wait()
			if outcome == "close" {
				s.Close()
			} else {
				unblock()
			}
			<-first.done
			if (outcome == "complete" || outcome == "redirect") && first.err != nil {
				t.Fatal(first.err)
			}
			if outcome != "complete" && outcome != "redirect" && first.err == nil {
				t.Fatal("expected failure")
			}
			snap, err := s.ActiveDownloads(context.Background())
			if err != nil || len(snap.Tasks) != 0 || snap.BytesPerSecond != 0 || calls.Load() != 1 {
				t.Fatalf("not cleaned or duplicate upstream: %+v %v calls=%d", snap, err, calls.Load())
			}
		})
	}
}

func TestActiveRateWindow(t *testing.T) {
	now := time.Unix(100, 0)
	p := &downloadProgress{stage: "download_and_hash", changed: now.Add(-10 * time.Second)}
	p.samples[0] = progressSample{second: 99, bytes: 500}
	p.samples[1] = progressSample{second: 94, bytes: 9000}
	if p.rate(now) != 100 || p.rate(now) != 100 {
		t.Fatal("rate includes expired bytes or depends on reads")
	}
	if p.rate(now.Add(5*time.Second)) != 0 {
		t.Fatal("stalled rate did not decay")
	}
	p.stage = "publish"
	if p.rate(now) != 0 {
		t.Fatal("publication counted as upstream traffic")
	}
	p.stage = "download_and_hash"
	p.changed = now.Add(-time.Second)
	if p.rate(now) != 500 {
		t.Fatal("startup rate denominator")
	}
}

func TestActiveSnapshotDoesNotJoinClose(t *testing.T) {
	s, _ := setup(t, func(http.ResponseWriter, *http.Request) {})
	s.wg.Add(1) // Keep shutdown blocked after cancellation, before database close.
	var release sync.Once
	defer release.Do(s.wg.Done)
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	<-s.ctx.Done()
	read := make(chan struct{})
	go func() {
		v, err := s.ActiveDownloads(context.Background())
		if err != nil || len(v.Tasks) != 0 {
			t.Errorf("closing snapshot: %+v %v", v, err)
		}
		close(read)
	}()
	select {
	case <-read:
	case <-time.After(time.Second):
		t.Fatal("snapshot joined shutdown")
	}
	release.Do(s.wg.Done)
	<-done
}
