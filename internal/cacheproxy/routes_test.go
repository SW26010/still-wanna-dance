package cacheproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouteCachesStayBounded(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {})
	target := videoURL(payload)
	r, _ := http.NewRequest(http.MethodGet, target, nil)
	v, err := s.parse(r)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.ResolveRoutes = func(_ context.Context, id string) ([]string, error) {
		return []string{strings.Replace(target, "1344-660524b4ebadb", id+"-mirror", 1)}, nil
	}
	for i := 1; i <= routeSongsLimit+10; i++ {
		v.songID = fmt.Sprint(i)
		s.routeCandidates(context.Background(), v)
	}
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	if len(s.routeCache.items) != routeCacheLimit || len(s.routeSongs.items) != 1 {
		t.Fatalf("cache sizes: routes=%d songs=%d", len(s.routeCache.items), len(s.routeSongs.items))
	}
}

func TestUnifiedRoutesOverridePlaybackAndPrefetch(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprint("background=", background), func(t *testing.T) {
			var mu sync.Mutex
			var downloaded []string
			var probes atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != "" {
					probes.Add(1)
					if r.Host == "play.udon.dance" {
						time.Sleep(60 * time.Millisecond)
					}
					w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(payload)-1, len(payload)))
					w.WriteHeader(206)
				}
				mu.Lock()
				downloaded = append(downloaded, r.Host+r.URL.Path)
				mu.Unlock()
				io.WriteString(w, payload)
			})
			cf := videoURL(payload)
			// Different paths must be kept intact; never manufacture a mirror URL.
			hkg := strings.Replace(strings.Replace(cf, "play.udon.dance", "nya.xin.moe", 1), "1344-660524b4ebadb", "9999-mirror", 1)
			var resolves atomic.Int32
			s.cfg.ResolveRoutes = func(_ context.Context, id string) ([]string, error) {
				if id != "42" {
					t.Errorf("resource number used as song ID: %s", id)
				}
				resolves.Add(1)
				return []string{cf, hkg}, nil
			}
			if background {
				if _, err := s.PrefetchSong(context.Background(), "42", cf); err != nil {
					t.Fatal(err)
				}
			} else {
				r, _ := http.NewRequest("GET", cf, nil)
				v, _ := s.parse(r)
				if err := s.recordVideo(context.Background(), v); err != nil {
					t.Fatal(err)
				}
				if err := s.recordSongVideo(context.Background(), "42", v); err != nil {
					t.Fatal(err)
				}
				w := request(s, "GET", cf, nil)
				if w.Code != 200 || w.Body.String() != payload {
					t.Fatalf("%d %q", w.Code, w.Body.String())
				}
			}
			mu.Lock()
			got := downloaded[len(downloaded)-1]
			mu.Unlock()
			want := "nya.xin.moe/files/2403/9999-mirror.mp4"
			if background {
				want = "play.udon.dance/files/2403/1344-660524b4ebadb.mp4"
				if probes.Load() != 1 {
					t.Fatal("background preference triggered latency probes")
				}
			}
			if got != want {
				t.Fatalf("download route=%s want=%s", got, want)
			}
			// A differently named request for the same bytes reuses route knowledge,
			// including the independently resolved mirror path and health samples.
			r, _ := http.NewRequest("GET", cf, nil)
			v, _ := s.parse(r)
			v.songID = "42"
			candidates := s.routeCandidates(context.Background(), v)
			s.rankRoutes(context.Background(), candidates)
			wantProbes := int32(3) // Two probes plus one content range.
			if background {
				wantProbes = 2 // One content range and one probe of the other host.
			}
			if resolves.Load() != 1 || probes.Load() != wantProbes {
				t.Fatalf("unnecessary re-probe: resolves=%d probes=%d", resolves.Load(), probes.Load())
			}
		})
	}
}

func TestRoutesRejectDifferentContentAndUnknownOwnership(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(fmt.Sprint("known=", known), func(t *testing.T) {
			var wrong atomic.Bool
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "play.udon.dance" {
					wrong.Store(true)
				}
				io.WriteString(w, payload)
			})
			var resolves atomic.Int32
			s.cfg.ResolveRoutes = func(context.Context, string) ([]string, error) {
				resolves.Add(1)
				return []string{strings.Replace(videoURL(strings.Repeat("x", len(payload))), "play.udon.dance", "nya.xin.moe", 1)}, nil
			}
			var err error
			if known {
				_, err = s.PrefetchSong(context.Background(), "42", videoURL(payload))
			} else {
				_, err = s.Prefetch(context.Background(), videoURL(payload))
			}
			if err != nil || wrong.Load() {
				t.Fatalf("unsafe content substitution: %v", err)
			}
			if !known && resolves.Load() != 0 {
				t.Fatal("inferred song from raw resource URL")
			}
		})
	}
}

func TestSelectedRouteHeaderFailureFallsBack(t *testing.T) {
	var mu sync.Mutex
	var hosts []string
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, r.Host)
		mu.Unlock()
		if r.Host == "play.udon.dance" {
			w.WriteHeader(503)
			return
		}
		io.WriteString(w, payload)
	})
	s.cfg.ResolveRoutes = func(context.Context, string) ([]string, error) {
		return []string{videoURL(payload), strings.Replace(videoURL(payload), "play.udon.dance", "nya.xin.moe", 1)}, nil
	}
	s.noteRoute("nya.xin.moe", time.Millisecond, false)
	s.noteRoute("play.udon.dance", time.Second, false)
	if _, err := s.PrefetchSong(context.Background(), "42", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := strings.Join(hosts, ",")
	mu.Unlock()
	if got != "play.udon.dance,nya.xin.moe" {
		t.Fatal(got)
	}
	s.routeMu.Lock()
	failed := s.routeHealth["play.udon.dance"].failed
	s.routeMu.Unlock()
	if !failed {
		t.Fatal("failed route was not cooled down")
	}
}
