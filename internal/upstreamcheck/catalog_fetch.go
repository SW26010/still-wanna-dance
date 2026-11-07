package upstreamcheck

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/catalogmeta"
)

// FetchCatalog starts every source together. Fetch must fully validate the
// catalog before returning success. HTTPS Udon wins as soon as it succeeds;
// otherwise wait for all sources and choose the newest valid version, keeping
// definition order for equal-priority ties. Accept is called exactly once for
// the winner. Primary success cancels the other requests. Workers are always
// drained before returning, including on caller cancellation or acceptance error.
func FetchCatalog(ctx context.Context, sources []string, fetch func(context.Context, string) (cacheproxy.Catalog, error), accept func(cacheproxy.Catalog) error) error {
	work, cancel := context.WithCancel(ctx)
	type result struct {
		index   int
		catalog cacheproxy.Catalog
		err     error
	}
	done := make(chan result, len(sources))
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	if err := ctx.Err(); err != nil {
		return err
	}
	for i, source := range sources {
		workers.Go(func() {
			catalog, err := fetch(work, source)
			done <- result{i, catalog, err}
		})
	}
	results := make([]result, len(sources))
	for range sources {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-done:
			if err := ctx.Err(); err != nil {
				return err
			}
			results[r.index] = r
			if r.err == nil && cacheproxy.IsPrimaryCatalog(r.catalog.Source) {
				cancel()
				return accept(r.catalog)
			}
		}
	}
	var best cacheproxy.Catalog
	var failures []error
	for _, r := range results {
		if r.err != nil {
			failures = append(failures, r.err)
		} else if best.Revision == "" || cacheproxy.CatalogPrecedes(r.catalog, best) {
			best = r.catalog
		}
	}
	if best.Revision != "" {
		return accept(best)
	}
	return errors.Join(failures...)
}

func checkCatalogs(ctx context.Context, access ConnectivityAccess, business ConnectivityBusiness, publish func(Result)) (int64, error) {
	routes := catalogmeta.SourceIDs()
	sources := make([]string, len(routes))
	type observation struct {
		result   Result
		response CatalogResponse
		song     int64
	}
	observations := make([]observation, len(routes))
	for i, route := range routes {
		sources[i] = catalogmeta.SourceURL(route)
		observations[i].result = Result{Operation: Catalog, Route: route, State: "checking"}
		publish(observations[i].result)
	}
	var selectedSong int64
	attemptedAcceptance := false
	fetchErr := FetchCatalog(ctx, sources, func(work context.Context, source string) (cacheproxy.Catalog, error) {
		i := slices.Index(sources, source)
		result, song, response := access.Catalog(work, routes[i])
		if work.Err() != nil {
			return cacheproxy.Catalog{}, work.Err()
		}
		var catalog cacheproxy.Catalog
		err := result.Err
		if len(response.Body) > 0 {
			response.Source, response.Route = source, routes[i]
			catalog, err = cacheproxy.ParseCatalog(response.Body, source)
			if err != nil {
				result.State, result.Err = "invalid", err
			}
		} else if err == nil {
			err = fmt.Errorf("清单接口不可用: %s", result.State)
		}
		if err != nil {
			slog.Warn("catalog_source_failed", "source", source, "error", err)
		}
		observations[i] = observation{result, response, song}
		publish(result)
		return catalog, err
	}, func(chosen cacheproxy.Catalog) error {
		attemptedAcceptance = true
		o := observations[slices.Index(sources, chosen.Source)]
		accepted, err := business.OnCatalog(ctx, []CatalogResponse{o.response})
		if err != nil {
			return err
		}
		if !accepted {
			return errors.New("清单未被接受")
		}
		o.result.Selected = true
		publish(o.result)
		selectedSong = o.song
		if o.song > 0 && business.RememberSample != nil {
			business.RememberSample(ctx, o.song)
		}
		return nil
	})
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if fetchErr == nil {
		for _, o := range observations {
			if o.result.State == "checking" {
				o.result.State = "skipped"
				publish(o.result)
			}
		}
	}
	if errors.Is(fetchErr, cacheproxy.ErrLocalStorage) {
		return 0, fetchErr
	}
	if fetchErr != nil && !attemptedAcceptance {
		var responses []CatalogResponse
		// Preserve existing business feedback for invalid bodies, without
		// allowing a network failure to cancel independent playback checks.
		for _, o := range observations {
			if len(o.response.Body) > 0 {
				responses = append(responses, o.response)
			}
		}
		if len(responses) > 0 {
			_, err := business.OnCatalog(ctx, responses)
			if ctx.Err() != nil || errors.Is(err, cacheproxy.ErrLocalStorage) {
				return 0, errors.Join(err, ctx.Err())
			}
		}
	}
	return selectedSong, nil
}
