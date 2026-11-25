package playbacksource

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type transport struct {
	id string
	do func(*http.Request) (*http.Response, error)
}

func TestResolverUsesHTTPAndKeepsSourceIdentity(t *testing.T) {
	const raw = "http://play.udon.dance/files/1/42-video.mp4?e=00000000000000000000000000000000&s=1"
	var schemes []string
	c := client("fixed-channel", func(r *http.Request) (*http.Response, error) {
		schemes = append(schemes, r.URL.Scheme)
		if r.URL.Scheme == "https" {
			return nil, io.EOF
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {raw}}, Body: http.NoBody, Request: r}, nil
	})
	r := Resolver{AttemptTimeout: time.Second, MaxFileBytes: 1024, Clients: func(context.Context, string) []*http.Client { return []*http.Client{c} }}
	a, err := r.Resolve(context.Background(), 42)
	if err != nil || a.URL != raw || a.API != DefaultBaseURL+"/Api/Songs/play" || a.ChannelID != "fixed-channel" || len(schemes) != 1 || schemes[0] != "http" {
		t.Fatal(a, schemes, err)
	}
}

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) { return t.do(r) }
func (t transport) ChannelID() string                                 { return t.id }

func client(id string, do func(*http.Request) (*http.Response, error)) *http.Client {
	return &http.Client{Transport: transport{id, do}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestResolverRetriesChannelsAndPreservesProvenance(t *testing.T) {
	const target = "https://nya.xin.moe/files/1/42-video.mp4?e=00000000000000000000000000000000&s=1"
	for _, badStatus := range []int{http.StatusServiceUnavailable, http.StatusFound} {
		calls := 0
		makeClient := func(id string, status int, location string) *http.Client {
			return client(id, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Query().Get("node") != "cf" || r.URL.Query().Get("id") != "42" {
					t.Fatalf("unexpected source: %s", r.URL)
				}
				if _, ok := r.Context().Deadline(); !ok {
					t.Fatal("attempt has no deadline")
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": {location}}, Body: http.NoBody, Request: r}, nil
			})
		}
		resolver := Resolver{MaxFileBytes: 1024, AttemptTimeout: time.Second, Clients: func(_ context.Context, endpoint string) []*http.Client {
			if endpoint != DefaultBaseURL+"/Api/Songs/play?node=cf" {
				t.Fatal(endpoint)
			}
			return []*http.Client{makeClient("failed", badStatus, "https://invalid.example/not-a-video"), makeClient("winner", http.StatusFound, target)}
		}}
		before := time.Now()
		address, err := resolver.Resolve(context.Background(), 42)
		if err != nil || calls != 2 || address.URL != target || address.Node != "cf" || address.ChannelID != "winner" || address.SongID != 42 || address.API != DefaultBaseURL+"/Api/Songs/play" {
			t.Fatalf("address=%+v calls=%d err=%v", address, calls, err)
		}
		if address.QueryStartedAt.Before(before) || address.ObservedAt.Before(address.QueryStartedAt) {
			t.Fatal("invalid evidence timestamps")
		}
	}
}

func TestResolverCancellationStopsChannelRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	c := client("channel", func(r *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return nil, r.Context().Err()
	})
	resolver := Resolver{MaxFileBytes: 1024, AttemptTimeout: time.Second, Clients: func(context.Context, string) []*http.Client { return []*http.Client{c, c} }}
	if _, err := resolver.Resolve(ctx, 42); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestResolverWithoutChannelsReturnsUnavailable(t *testing.T) {
	resolver := Resolver{MaxFileBytes: 1024, AttemptTimeout: time.Second, Clients: func(context.Context, string) []*http.Client { return nil }}
	if _, err := resolver.Resolve(context.Background(), 42); err == nil {
		t.Fatal("accepted missing channels")
	}
}

func TestResolverSlowValidPathSurvivesOtherCandidates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const location = "https://play.udon.dance/files/1/42-video.mp4?e=00000000000000000000000000000000&s=1"
		var active atomic.Int32
		var attempts atomic.Int32
		var paths []*http.Client
		for i := range 6 {
			paths = append(paths, client("test", func(r *http.Request) (*http.Response, error) {
				active.Add(1)
				defer active.Add(-1)
				attempts.Add(1)
				if i == 0 {
					select {
					case <-time.After(3 * time.Second):
						return &http.Response{StatusCode: 302, Header: http.Header{"Location": {location}}, Body: http.NoBody}, nil
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}
				<-r.Context().Done()
				return nil, r.Context().Err()
			}))
		}
		resolver := Resolver{MaxFileBytes: 1024, AttemptTimeout: 10 * time.Second, Clients: func(context.Context, string) []*http.Client { return paths }}
		address, err := resolver.Resolve(t.Context(), 42)
		if err != nil || address.URL != location || attempts.Load() != 2 || active.Load() != 0 {
			t.Fatalf("err=%v attempts=%d active=%d", err, attempts.Load(), active.Load())
		}
	})
}

func TestResolverDiscoveryAndReadsShareDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		path := client("slow", func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		resolver := Resolver{AttemptTimeout: 10 * time.Second, Clients: func(context.Context, string) []*http.Client {
			time.Sleep(4 * time.Second)
			return []*http.Client{path}
		}}
		_, err := resolver.Resolve(t.Context(), 42)
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != resolver.AttemptTimeout {
			t.Fatalf("elapsed=%s err=%v", time.Since(start), err)
		}
	})
}
