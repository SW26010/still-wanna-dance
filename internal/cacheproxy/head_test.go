package cacheproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestHEADDoesNotRequireDownloadCapacity(t *testing.T) {
	s, _ := setup(t, func(http.ResponseWriter, *http.Request) {
		t.Error("HEAD reached the media upstream")
	})
	s.mu.Lock()
	err := s.reserveUseLocked(useSpan{"occupied", 0, backgroundUseBlocks}, false)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	response := request(s, http.MethodHead, videoURL(payload), nil)
	assertResponse(t, response, http.StatusOK, "")
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.useBlocks) != backgroundUseBlocks || len(s.demands) != 0 || len(s.flights) != 0 {
		t.Fatal("HEAD changed download demand or capacity")
	}
}

func TestHEADDoesNotRegisterDownloadDemand(t *testing.T) {
	for _, song := range []bool{false, true} {
		t.Run(fmt.Sprintf("song=%v", song), func(t *testing.T) {
			var upstream atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				upstream.Add(1)
				serveFixtureContent(w, r, payload)
			}, func(cfg *Config) {
				cfg.ResolvePlayback = func(context.Context, string) (string, error) { return videoURL(payload), nil }
			})
			target := videoURL(payload)
			if song {
				target = "http://api.udon.dance/Api/Songs/play?id=42"
			}
			capacity := s.DemandCapacityChanged()
			assertResponse(t, request(s, http.MethodHead, target, nil), http.StatusOK, "")
			select {
			case <-capacity:
				t.Error("HEAD registered and released download capacity")
			default:
			}
			s.mu.Lock()
			if len(s.demands) != 0 || len(s.useBlocks) != 0 || len(s.flights) != 0 {
				t.Error("HEAD left download demand or work")
			}
			s.mu.Unlock()
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if upstream.Load() != 0 {
				t.Fatal("HEAD started a media request", upstream.Load())
			}
		})
	}
}

func TestHEADMetadataAndConditions(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprintf("local=%v", local), func(t *testing.T) {
			s, cfg := setup(t, func(http.ResponseWriter, *http.Request) { t.Error("HEAD reached upstream") })
			v := parsedVideo(t, s, payload)
			cache := "MISS"
			if local {
				writeTestFile(t, cfg.videoFile(v.key), payload)
				cache = "HIT"
			}
			initial := request(s, http.MethodHead, videoURL(payload), nil)
			assertResponse(t, initial, http.StatusOK, "")
			preview := httptest.NewRecorder()
			PreviewHeaders(preview, PreviewTarget{Resource: v.key, Size: v.size})
			for key, want := range map[string]string{
				"Content-Type": "video/mp4", "ETag": `"` + v.key + `"`,
				"Accept-Ranges": "bytes", "Content-Length": fmt.Sprint(len(payload)),
			} {
				if got := initial.Header().Get(key); got != want {
					t.Fatalf("HEAD %s: got %q, want %q", key, got, want)
				}
				if got := preview.Header().Get(key); got != want {
					t.Fatalf("preview %s: got %q, want %q", key, got, want)
				}
			}
			if initial.Header().Get("X-SWD-Cache") != cache {
				t.Fatal(initial.Header())
			}
			type condition struct {
				headers map[string]string
				status  int
			}
			cases := []condition{
				{map[string]string{"Range": "bytes=2-4", "If-Range": `"other"`}, http.StatusOK},
				{map[string]string{"Range": "bytes=0-1,3-4"}, http.StatusOK},
				{map[string]string{"Range": "bytes=invalid"}, http.StatusOK},
				{map[string]string{"If-None-Match": initial.Header().Get("ETag")}, http.StatusNotModified},
				{map[string]string{"If-Match": `"other"`}, http.StatusPreconditionFailed},
			}
			if local {
				cases = append(cases, condition{map[string]string{"If-Modified-Since": initial.Header().Get("Last-Modified")}, http.StatusNotModified})
			}
			for _, tc := range cases {
				response := request(s, http.MethodHead, videoURL(payload), tc.headers)
				assertResponse(t, response, tc.status, "")
				if response.Header().Get("Content-Range") != "" {
					t.Fatal("HEAD returned a partial range", response.Header())
				}
			}
		})
	}
}
