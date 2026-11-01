package upstreamcheck

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/playbacksource"
)

const connectivityCachedURL = "http://play.udon.dance/files/1/42-video.mp4?e=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&s=1234"

func waitConnectivitySignal[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatal("timed out waiting for connectivity work")
		var zero T
		return zero
	}
}

func TestConnectivityFullStartsThreeChecksTogetherWithCachedSamples(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := make(chan Operation, 3)
	release, done := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	sample, err := parseSample(connectivityCachedURL)
	if err != nil {
		t.Fatal(err)
	}
	var catalogWrites, playbackWrites, resourceRequests atomic.Int32
	access := ConnectivityAccess{
		Catalog: func(ctx context.Context, route string) (Result, int64, CatalogResponse) {
			started <- Catalog
			select {
			case <-release:
			case <-ctx.Done():
				return Result{}, 0, CatalogResponse{}
			}
			return Result{Operation: Catalog, Route: route, State: "available"}, 42, CatalogResponse{Route: route, Body: []byte("valid")}
		},
		Playback: func(ctx context.Context, id int64, route playbacksource.Entry) (Result, *Sample, PlaybackURLResponse) {
			started <- PlaybackURL
			select {
			case <-release:
			case <-ctx.Done():
				return Result{}, nil, PlaybackURLResponse{}
			}
			s := sample.sample()
			return Result{Operation: PlaybackURL, Route: route.ID, State: "available"}, &s, PlaybackURLResponse{SongID: id, URL: connectivityCachedURL}
		},
		Resource: func(ctx context.Context, _ int64, s Sample) Result {
			resourceRequests.Add(1)
			started <- Resource
			select {
			case <-release:
			case <-ctx.Done():
				return Result{}
			}
			return Result{Operation: Resource, Route: s.Host, State: "available"}
		},
	}
	business := ConnectivityBusiness{
		CachedSong: func(context.Context) int64 { return 42 }, CachedResource: func(context.Context) string { return connectivityCachedURL },
		SelectSong:    func(_ context.Context, fallback int64) int64 { return fallback },
		OnCatalog:     func(context.Context, []CatalogResponse) (bool, error) { catalogWrites.Add(1); return true, nil },
		OnPlaybackURL: func(context.Context, PlaybackURLResponse) { playbackWrites.Add(1) },
	}
	var results []Result
	go func() {
		defer close(done)
		CheckConnectivity(ctx, access, business, "", func(r Result) { results = append(results, r) })
	}()
	seen := map[Operation]bool{}
	for range 3 {
		seen[waitConnectivitySignal(t, ctx, started)] = true
	}
	if !seen[Catalog] || !seen[PlaybackURL] || !seen[Resource] {
		t.Fatal("checks did not start together", seen)
	}
	close(release)
	waitConnectivitySignal(t, ctx, done)
	if catalogWrites.Load() != 1 || playbackWrites.Load() != 1 || resourceRequests.Load() != 1 {
		t.Fatal("business responses were not reused or resource was probed twice", catalogWrites.Load(), playbackWrites.Load(), resourceRequests.Load())
	}
	for _, op := range []Operation{Catalog, PlaybackURL, Resource} {
		found := false
		for _, result := range results {
			found = found || result.Operation == op && result.State == "available"
		}
		if !found {
			t.Fatal("missing completed observation", op, results)
		}
	}
}

func TestConnectivityFullWaitsForMissingSamples(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	catalogStarted, playbackStarted, resourceStarted := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{}, 1)
	releaseCatalog, releasePlayback, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	sample, err := parseSample(connectivityCachedURL)
	if err != nil {
		t.Fatal(err)
	}
	access := ConnectivityAccess{
		Catalog: func(ctx context.Context, route string) (Result, int64, CatalogResponse) {
			catalogStarted <- struct{}{}
			select {
			case <-releaseCatalog:
			case <-ctx.Done():
				return Result{}, 0, CatalogResponse{}
			}
			return Result{Operation: Catalog, Route: route, State: "available"}, 42, CatalogResponse{Route: route, Body: []byte("valid")}
		},
		Playback: func(ctx context.Context, id int64, route playbacksource.Entry) (Result, *Sample, PlaybackURLResponse) {
			playbackStarted <- struct{}{}
			select {
			case <-releasePlayback:
			case <-ctx.Done():
				return Result{}, nil, PlaybackURLResponse{}
			}
			s := sample.sample()
			return Result{Operation: PlaybackURL, Route: route.ID, State: "available"}, &s, PlaybackURLResponse{SongID: id, URL: connectivityCachedURL}
		},
		Resource: func(_ context.Context, _ int64, s Sample) Result {
			resourceStarted <- struct{}{}
			return Result{Operation: Resource, Route: s.Host, State: "available"}
		},
	}
	business := ConnectivityBusiness{
		SelectSong:    func(_ context.Context, fallback int64) int64 { return fallback },
		OnCatalog:     func(context.Context, []CatalogResponse) (bool, error) { return true, nil },
		OnPlaybackURL: func(context.Context, PlaybackURLResponse) {},
	}
	go func() { defer close(done); CheckConnectivity(ctx, access, business, "", func(Result) {}) }()
	waitConnectivitySignal(t, ctx, catalogStarted)
	select {
	case <-playbackStarted:
		t.Fatal("playback started without a song")
	default:
	}
	select {
	case <-resourceStarted:
		t.Fatal("resource started without an address")
	default:
	}
	close(releaseCatalog)
	waitConnectivitySignal(t, ctx, playbackStarted)
	select {
	case <-resourceStarted:
		t.Fatal("resource started before playback resolved an address")
	default:
	}
	close(releasePlayback)
	waitConnectivitySignal(t, ctx, resourceStarted)
	waitConnectivitySignal(t, ctx, done)
}

func TestConnectivityFullRetriesFailedCachedResourceWithFreshAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	newURL := "http://play.udon.dance/files/1/42-new.mp4?e=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb&s=1234"
	sample, err := parseSample(newURL)
	if err != nil {
		t.Fatal(err)
	}
	oldChecked := make(chan struct{})
	var urls []string
	access := ConnectivityAccess{
		Catalog: func(_ context.Context, route string) (Result, int64, CatalogResponse) {
			return Result{Operation: Catalog, Route: route, State: "available"}, 42, CatalogResponse{Route: route, Body: []byte("valid")}
		},
		Playback: func(ctx context.Context, id int64, route playbacksource.Entry) (Result, *Sample, PlaybackURLResponse) {
			select {
			case <-oldChecked:
			case <-ctx.Done():
				return Result{}, nil, PlaybackURLResponse{}
			}
			s := sample.sample()
			return Result{Operation: PlaybackURL, Route: route.ID, State: "available"}, &s, PlaybackURLResponse{SongID: id, URL: newURL}
		},
		Resource: func(_ context.Context, _ int64, s Sample) Result {
			urls = append(urls, s.URL)
			if s.URL == connectivityCachedURL {
				close(oldChecked)
				return Result{Operation: Resource, Route: s.Host, State: "failed"}
			}
			return Result{Operation: Resource, Route: s.Host, State: "available"}
		},
	}
	business := ConnectivityBusiness{
		CachedSong: func(context.Context) int64 { return 42 }, CachedResource: func(context.Context) string { return connectivityCachedURL },
		SelectSong:    func(_ context.Context, fallback int64) int64 { return fallback },
		OnCatalog:     func(context.Context, []CatalogResponse) (bool, error) { return true, nil },
		OnPlaybackURL: func(context.Context, PlaybackURLResponse) {},
	}
	var resourceStates []string
	CheckConnectivity(ctx, access, business, "", func(r Result) {
		if r.Operation == Resource && r.State != "checking" {
			resourceStates = append(resourceStates, r.State)
		}
	})
	if len(urls) != 2 || urls[0] != connectivityCachedURL || urls[1] != newURL || len(resourceStates) != 2 || resourceStates[0] != "failed" || resourceStates[1] != "available" {
		t.Fatal("cached failure did not fall back to the fresh address", urls, resourceStates)
	}
}
