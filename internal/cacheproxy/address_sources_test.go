package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSongRetryKeepsSelectedResource(t *testing.T) {
	for _, queued := range []bool{false, true} {
		for _, changed := range []bool{false, true} {
			t.Run(fmt.Sprintf("queue=%v/changed=%v", queued, changed), func(t *testing.T) {
				body := payload
				if changed {
					body = strings.Repeat("z", len(payload))
				}
				freshURL := videoURL(body) + "&address=fresh"
				oldRequested := make(chan struct{})
				var once sync.Once
				var freshRequests atomic.Int32
				var s *Server
				s, _ = setup(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Query().Get("address") == "old" {
						once.Do(func() { close(oldRequested) })
						http.Error(w, "denied", http.StatusForbidden)
						return
					}
					freshRequests.Add(1)
					io.WriteString(w, body)
				}, func(cfg *Config) {
					cfg.CacheUnlimited = true
					cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
						select {
						case <-oldRequested:
						case <-ctx.Done():
							return "", ctx.Err()
						}
						return freshURL, s.ObserveSongURL(ctx, PlaybackObservation{SongID: 42, Node: "cf", URL: freshURL})
					}
				})
				if err := s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: 42, Node: "cf", URL: videoURL(payload) + "&address=old"}); err != nil {
					t.Fatal(err)
				}
				if queued {
					_, err := s.PrefetchPlayback(t.Context(), 42)
					if (err != nil) != changed {
						t.Fatalf("changed=%v, prefetch error=%v", changed, err)
					}
				} else {
					w := request(s, http.MethodGet, "http://api.udon.dance/Api/Songs/play?id=42", nil)
					if changed && w.Code == http.StatusOK || !changed && (w.Code != http.StatusOK || w.Body.String() != body) {
						t.Fatalf("changed=%v, status=%d body=%q", changed, w.Code, w.Body.String())
					}
				}
				if changed && freshRequests.Load() != 0 || !changed && freshRequests.Load() == 0 {
					t.Fatal("unexpected fresh-resource requests", freshRequests.Load())
				}
				assertSongResource(t, s, "42", parsedVideo(t, s, body).key)
			})
		}
	}
}

func TestPlaybackCheckSharesSongAndSurvivesWaiterCancellation(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	s, _ := setup(t, nil, func(cfg *Config) {
		cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
			if id != "42" {
				t.Errorf("song ID = %s", id)
			}
			close(started)
			select {
			case <-finish:
				return videoURL(payload), nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	check := s.startPlaybackCheck(ctx, "042", video{})
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("resolution did not start")
	}
	if s.startPlaybackCheck(ctx, "42", video{}) != check {
		t.Fatal("duplicate query for the same song")
	}
	waiter, stop := context.WithCancel(ctx)
	stop()
	if _, err := s.waitSongQuery(waiter, check); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(finish)
	if v, err := s.waitSongQuery(ctx, check); err != nil || v.songID != "42" {
		t.Fatalf("shared query canceled: %+v %v", v, err)
	}
}

func TestResourceAddressEligibilityPrecedesCandidateLimit(t *testing.T) {
	var queries atomic.Int32
	s, _ := setup(t, nil, func(cfg *Config) {
		cfg.ResolvePlayback = func(context.Context, string) (string, error) {
			queries.Add(1)
			return "", errors.New("API unavailable")
		}
	})
	target := videoURL(payload)
	v, err := s.parse(httptest.NewRequest(http.MethodGet, target, nil))
	if err != nil {
		t.Fatal(err)
	}

	// More recent disallowed observations must not consume the 32 slots and
	// hide an older usable address. Historical evidence remains inspectable.
	for id := int64(1); id <= 34; id++ {
		node := fmt.Sprintf("other%d", id)
		if id == 1 {
			node = "cf"
		}
		if err := s.ObserveSongURL(t.Context(), PlaybackObservation{SongID: id, URL: target, Node: node}); err != nil {
			t.Fatal(err)
		}
	}
	addresses := resourceAddresses{key: v.key, size: v.size}
	got, err := addresses.next(t.Context(), s, video{})
	if err != nil || !got.savedAddress || got.songID != "" || queries.Load() != 0 {
		t.Fatalf("address=%+v queries=%d err=%v", got, queries.Load(), err)
	}
	all, err := s.ResourceURLs(t.Context(), v.key, "", 40)
	if err != nil || len(all) != 34 {
		t.Fatalf("historical observations=%d err=%v", len(all), err)
	}
}

func TestResourcePreviewKeepsAliasLimit(t *testing.T) {
	var queries atomic.Int32
	s, _ := setup(t, nil, func(cfg *Config) {
		cfg.ResolvePlayback = func(context.Context, string) (string, error) {
			queries.Add(1)
			return "", errors.New("unavailable")
		}
	})
	target := seedPreview(t, s, payload)
	for id := 2; id <= 9; id++ {
		if _, err := s.db.Exec("INSERT INTO songs(id) VALUES (?)", id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("INSERT INTO song_media(song_id,md5) VALUES (?,?)", id, target.Resource); err != nil {
			t.Fatal(err)
		}
	}
	addresses := resourceAddresses{key: target.Resource, size: target.Size}
	if _, err := addresses.next(t.Context(), s, video{}); err == nil || queries.Load() != 8 {
		t.Fatalf("queries=%d error=%v", queries.Load(), err)
	}
}

func TestResourceAddressServerStopBeforeWaiterCancellation(t *testing.T) {
	for _, storageFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("storage_failure=%v", storageFailure), func(t *testing.T) {
			started := make(chan struct{})
			s, _ := setup(t, nil, func(cfg *Config) {
				cfg.ResolvePlayback = func(ctx context.Context, _ string) (string, error) {
					close(started)
					<-ctx.Done()
					if storageFailure {
						return "", errors.Join(ErrLocalStorage, ctx.Err())
					}
					return "", ctx.Err()
				}
			})
			target := seedPreview(t, s, payload)
			check := s.startPlaybackCheck(t.Context(), "1", video{})
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("lookup did not start")
			}
			s.cancel()
			select {
			case <-check.done:
			case <-time.After(5 * time.Second):
				t.Fatal("lookup did not stop")
			}
			_, lookupErr := s.waitSongQuery(t.Context(), check)
			if !errors.Is(lookupErr, context.Canceled) || !storageFailure && !errors.Is(lookupErr, errPlaybackUpstream) {
				t.Fatalf("lookup error = %v", lookupErr)
			}
			// The server-owned lookup can finish before its asynchronous cancellation
			// reaches the request waiting for a fixed resource's address.
			addresses := resourceAddresses{key: target.Resource, size: target.Size, preferredSong: "1", preferredCheck: check, batch: true}
			_, err := addresses.next(t.Context(), s, video{})
			if !errors.Is(err, context.Canceled) || storageFailure && (!errors.Is(err, ErrLocalStorage) || errors.Is(err, errPlaybackUpstream)) {
				t.Fatalf("resource address error = %v", err)
			}
		})
	}
}

func TestResourceAddressRetriesAfterIndependentLookupFailure(t *testing.T) {
	for _, lookupErr := range []error{context.Canceled, context.DeadlineExceeded, errors.New("API unavailable")} {
		t.Run(lookupErr.Error(), func(t *testing.T) {
			calls := make(chan string, 2)
			s, _ := setup(t, nil, func(cfg *Config) {
				cfg.ResolvePlayback = func(_ context.Context, id string) (string, error) {
					calls <- id
					if id == "1" {
						return "", fmt.Errorf("lookup failed: %w", lookupErr)
					}
					return videoURL(payload), nil
				}
			})
			target := seedPreview(t, s, payload)
			if _, err := s.db.Exec("INSERT INTO songs(id) VALUES(2)"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("INSERT INTO song_media(song_id,md5) VALUES(2,?)", target.Resource); err != nil {
				t.Fatal(err)
			}
			addresses := resourceAddresses{key: target.Resource, size: target.Size, preferredSong: "1", batch: true}
			v, err := addresses.next(t.Context(), s, video{})
			if err != nil || v.key != target.Resource || len(calls) != 2 || <-calls != "1" || <-calls != "2" {
				t.Fatalf("address=%+v lookup calls=%d error=%v", v, len(calls), err)
			}
		})
	}
}
