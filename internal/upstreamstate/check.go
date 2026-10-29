package upstreamstate

import (
	"context"
	"sync"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

// checkRoutes owns candidate discovery and the catalog -> playback -> resource
// flow. Separate monitors own separate configuration and observation state.
func (m *Monitor) checkRoutes(ctx context.Context, p Policy, source upstreamrequest.Snapshot, record func(observation)) {
	kind := m.batchKind
	started := time.Now()
	provider := source.Candidates
	publish := func(o observation, c upstreamrequest.Candidate) {
		o.channel = c.ID
		m.mu.Lock()
		defer m.mu.Unlock()
		record(o)
	}
	// Limit concurrent transfers regardless of the number of DNS answers.
	parallel := func(cs []upstreamrequest.Candidate, limit int, f func(int, upstreamrequest.Candidate)) {
		sem := make(chan struct{}, limit)
		var wg sync.WaitGroup
		for i, c := range cs {
			if ctx.Err() != nil {
				break
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				wg.Wait()
				return
			}
			wg.Add(1)
			go func(i int, c upstreamrequest.Candidate) { defer wg.Done(); defer func() { <-sem }(); f(i, c) }(i, c)
		}
		wg.Wait()
	}
	var preparedMu sync.Mutex
	prepared := make(map[string]bool)
	prepare := func(target string) []upstreamrequest.Candidate {
		qctx, cancel := context.WithTimeout(ctx, p.RequestTimeout)
		defer cancel()
		cs, err := provider.Candidates(qctx, target)
		ready := err == nil && qctx.Err() == nil
		preparedMu.Lock()
		prepared[target] = ready
		preparedMu.Unlock()
		return cs
	}

	var ids []int64
	var catalogs []CatalogResponse
	reusedSong := false
	if !kind.includes(Catalog) {
		if id := m.cachedSong(p); id > 0 {
			ids = append(ids, id)
			reusedSong = true
		}
	}
	for _, route := range routeIDs(Catalog) {
		if !kind.includes(Catalog) {
			found := false
			for _, id := range ids {
				found = found || id > 0
			}
			if found {
				break
			}
		}
		if ctx.Err() != nil {
			break
		}
		cs := prepare(entry(Catalog, route))
		samples := make([]int64, len(cs))
		responses := make([]CatalogResponse, len(cs))
		parallel(cs, 4, func(i int, c upstreamrequest.Candidate) {
			o, id, response := probeCatalogResponse(ctx, requestClient(c.Transport), p, route)
			responses[i] = response
			samples[i] = id
			publish(o, c)
		})
		ids = append(ids, samples...)
		for _, response := range responses {
			if len(response.Body) > 0 {
				catalogs = append(catalogs, response)
			}
		}
	}

	m.mu.Lock()
	if m.refreshChannelLocked().Revision != source.Revision {
		m.mu.Unlock()
		return
	}
	for _, id := range ids {
		if reusedSong {
			break
		}
		if id > 0 {
			m.songID, m.songAt = id, time.Now()
			break
		}
	}
	if !time.Now().Before(m.songAt.Add(p.SampleLifetime)) {
		m.songID = 0
	}
	id := m.songID
	m.mu.Unlock()
	m.deliverCatalogs(ctx, source.Revision, catalogs)
	id = m.selectPlaybackSong(ctx, source.Revision, kind, id)
	if id > 0 && kind != CheckCatalog && ctx.Err() == nil {
		m.publishProgress(ctx, PlaybackURL)
		samples := make(chan videoSample)
		go func() {
			var routes sync.WaitGroup
			for _, r := range playbackRoutes {
				routes.Add(1)
				go func(r route) {
					defer routes.Done()
					cs := prepare(entry(PlaybackURL, r.ID))
					// Bound parallel checks of the playback entry's API channels.
					parallel(cs, 2, func(_ int, c upstreamrequest.Candidate) {
						o, sample, response := probePlaybackResponse(ctx, requestClient(c.Transport), p, id, r)
						publish(o, c)
						m.deliverPlaybackURL(ctx, source.Revision, response)
						if kind != CheckPlayback && sample != nil {
							select {
							case samples <- *sample:
							case <-ctx.Done():
							}
						}
					})
				}(r)
			}
			routes.Wait()
			close(samples)
		}()
		seen := make(map[string]bool)
		// Consume as soon as an API channel yields a sample. Resource probes remain
		// serial through body close, including across domains and candidates.
		for sample := range samples {
			if seen[sample.host] || ctx.Err() != nil {
				continue
			}
			if len(seen) == 0 {
				m.publishProgress(ctx, Resource)
			}
			seen[sample.host] = true
			for _, c := range prepare(entry(Resource, sample.host)) {
				if ctx.Err() != nil {
					break
				}
				publish(m.probeResourceWhenIdle(ctx, requestClient(c.Transport), p, id, sample), c)
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshChannelLocked().Revision == source.Revision {
		m.updatePreferencesLocked(provider, started, kind)
		valid := make(map[string]bool)
		for _, op := range operations {
			for _, route := range m.routeIDsLocked(op) {
				for _, c := range provider.Current(entry(op, route)) {
					valid[string(op)+"/"+route+"/"+c.ID] = true
				}
			}
		}
		for key := range m.history {
			if !valid[key] {
				delete(m.history, key)
			}
		}
		for key := range m.diagnostics {
			if !valid[key] {
				delete(m.diagnostics, key)
			}
		}
		for key := range m.resourceSources {
			if !valid[key] {
				m.removeResourceSourceLocked(key)
			}
		}
		// Restored samples need not have any entry in history. Only prune
		// confirmed candidate sets. Failed discovery and unqueried routes wait.
		if ctx.Err() == nil {
			m.pruneThroughputLocked(valid, prepared, provider)
		}
	}
}
