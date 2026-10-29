package playbacksource

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

type transport struct {
	id string
	do func(*http.Request) (*http.Response, error)
}

func TestResolverProtocolFallbackKeepsSourceIdentity(t *testing.T) {
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
	if err != nil || a.URL != raw || a.API != DefaultBaseURL+"/Api/Songs/play" || a.ChannelID != "fixed-channel" || len(schemes) != 2 || schemes[0] != "https" || schemes[1] != "http" {
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
