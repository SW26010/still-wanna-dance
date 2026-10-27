package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

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
	check := s.startPlaybackCheck("042", video{})
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("resolution did not start")
	}
	if s.startPlaybackCheck("42", video{}) != check {
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
	old := time.Now().Add(-time.Minute)
	// More recent disallowed observations must not consume the 32 slots and
	// hide an older usable address. Historical evidence remains inspectable.
	for id := int64(1); id <= 34; id++ {
		node := "nya"
		if id == 1 {
			node = "cf"
		}
		observed := old.Add(time.Duration(id) * time.Millisecond)
		if err := s.ObserveSongURL(t.Context(), SongURL{SongID: id, URL: target, API: "https://api.udon.dance/Api/Songs/play", Node: node, QueryStartedAt: observed, ObservedAt: observed}); err != nil {
			t.Fatal(err)
		}
	}
	addresses := resourceAddresses{key: v.key, size: v.size}
	got, err := addresses.next(t.Context(), s, video{})
	if err != nil || got.cached == nil || got.cached.Node != "cf" || got.songID != "" || queries.Load() != 0 {
		t.Fatalf("address=%+v queries=%d err=%v", got, queries.Load(), err)
	}
	all, err := s.ResourceURLs(t.Context(), v.key, "", 40)
	if err != nil || len(all) != 34 {
		t.Fatalf("historical observations=%d err=%v", len(all), err)
	}
}
