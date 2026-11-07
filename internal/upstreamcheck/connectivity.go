package upstreamcheck

import (
	"context"
	"sync"

	"still-wanna-dance/internal/playbacksource"
)

// ConnectivityAccess is assembled by the application. Each operation owns its
// bounded candidate retries; the check never discovers channels.
type ConnectivityAccess struct {
	Catalog  func(context.Context, string) (Result, int64, CatalogResponse)
	Playback func(context.Context, int64, playbacksource.Entry) (Result, *Sample, PlaybackURLResponse)
	Resource func(context.Context, int64, Sample) Result
}

// ConnectivityBusiness consumes the same responses that produced observations.
// Selection and persistence are business decisions, not network health signals.
type ConnectivityBusiness struct {
	// OnCatalog reports acceptance separately from a local execution failure.
	// It receives the selected valid catalog, or invalid bodies for error feedback.
	// Storage/cancellation errors stop the run.
	OnCatalog      func(context.Context, []CatalogResponse) (bool, error)
	RememberSample func(context.Context, int64)
	SelectSong     func(context.Context, int64) int64
	OnPlaybackURL  func(context.Context, PlaybackURLResponse)
	CachedSong     func(context.Context) int64
	CachedResource func(context.Context) string
}

// CheckConnectivity shares one flow across automatic, full and scoped checks.
// Full checks start independently when local samples are available; otherwise
// playback and resource checks wait only for the samples they need.
func CheckConnectivity(ctx context.Context, access ConnectivityAccess, business ConnectivityBusiness, operation Operation, publish func(Result)) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var publishMu sync.Mutex
	emit := func(result Result) {
		publishMu.Lock()
		defer publishMu.Unlock()
		if ctx.Err() == nil {
			publish(result)
		}
	}
	checking := func(op Operation, route string) { emit(Result{Operation: op, Route: route, State: "checking"}) }
	checkCatalog := func() (int64, error) { return checkCatalogs(ctx, access, business, emit) }
	if operation == Catalog {
		_, _ = checkCatalog()
		return
	}

	type catalogResult struct {
		id  int64
		err error
	}
	type resourceSample struct {
		id     int64
		sample Sample
	}
	full := operation == ""
	wantPlayback := full || operation == PlaybackURL
	wantResource := full || operation == Resource
	catalogDone := make(chan catalogResult, 1)
	samples := make(chan resourceSample, len(playbacksource.Entries()))
	var workers sync.WaitGroup
	if full {
		workers.Go(func() {
			id, err := checkCatalog()
			if err != nil {
				cancel()
			}
			catalogDone <- catalogResult{id, err}
		})
	}
	if wantResource {
		workers.Go(func() {
			type attempted struct {
				url       string
				available bool
			}
			seen := make(map[string]attempted)
			check := func(id int64, sample Sample) {
				if prior, ok := seen[sample.Host]; ok && (prior.available || prior.url == sample.URL) {
					return
				}
				if ctx.Err() != nil {
					return
				}
				checking(Resource, sample.Host)
				result := access.Resource(ctx, id, sample)
				if ctx.Err() != nil {
					return
				}
				emit(result)
				seen[sample.Host] = attempted{sample.URL, result.State == "available"}
			}
			if full && business.CachedResource != nil {
				if saved, err := parseSample(business.CachedResource(ctx)); err == nil {
					check(0, saved.sample())
				}
			}
			for sample := range samples {
				check(sample.id, sample.sample)
			}
			if len(seen) == 0 && ctx.Err() == nil {
				emit(Result{Operation: Resource, State: "skipped"})
			}
		})
	}
	workers.Go(func() {
		defer close(samples)
		var fallback int64
		if full && business.CachedSong != nil {
			fallback = business.CachedSong(ctx)
		}
		id := business.SelectSong(ctx, fallback)
		if id <= 0 && full {
			select {
			case catalog := <-catalogDone:
				if catalog.err != nil {
					return
				}
				if catalog.id > 0 {
					id = business.SelectSong(ctx, catalog.id)
				}
			case <-ctx.Done():
				return
			}
		}
		if id <= 0 {
			if wantPlayback && ctx.Err() == nil {
				emit(Result{Operation: PlaybackURL, State: "skipped"})
			}
			return
		}
		for _, route := range playbacksource.Entries() {
			if ctx.Err() != nil {
				return
			}
			if wantPlayback {
				checking(PlaybackURL, route.ID)
			}
			result, sample, response := access.Playback(ctx, id, route)
			if ctx.Err() != nil {
				return
			}
			if wantPlayback {
				emit(result)
			}
			if wantResource && sample != nil {
				select {
				case samples <- resourceSample{id, *sample}:
				case <-ctx.Done():
					return
				}
			}
			if response.URL != "" && ctx.Err() == nil {
				business.OnPlaybackURL(ctx, response)
			}
		}
	})
	workers.Wait()
}
