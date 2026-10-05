package upstreamstate

import (
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

func (m *Monitor) playbackSource(o observation) upstreamrequest.ResourceSource {
	s := o.resourceSource
	s.APIChannelID = o.channel
	s.ObservedAt = o.at
	s.ValidUntil = o.at.Add(m.policy.SampleLifetime)
	return s
}

// Called under mu. Domain aggregation never discards the issuing API/channel
// or the exact returned URL. Failed/replaced/expired sources are excluded.
func (m *Monitor) sourcesLocked(op Operation, id string, now time.Time) []upstreamrequest.ResourceSource {
	m.pruneResourceSourcesLocked(now)
	var sources []upstreamrequest.ResourceSource
	for _, o := range m.resourceSources {
		if o.resourceSource.ResourceURL == "" || !now.Before(o.at.Add(m.policy.SampleLifetime)) {
			continue
		}
		if op == Resource && o.resourceHost == id || op == PlaybackURL && o.route == id {
			sources = append(sources, m.playbackSource(o))
		}
	}
	upstreamrequest.SortResourceSources(sources)
	return sources
}

// Keep monitor membership and the shared authorization set in sync even while
// a check is blocked on unrelated work. Snapshot reads do not wait for a batch.
func (m *Monitor) pruneResourceSourcesLocked(now time.Time) {
	snapshot := m.channel.Snapshot()
	for key, o := range m.resourceSources {
		source := m.playbackSource(o)
		if source.API == "" {
			source.API = entry(PlaybackURL, o.route)
		}
		if !upstreamrequest.ResourceSourceActive(source, snapshot.Candidates, now) {
			m.removeResourceSourceLocked(key)
		}
	}
}

// ResourceSources returns exact current addresses and their issuing APIs for
// internal business use. Result.Sources exposes the same provenance in status
// JSON without serializing signed resource URLs.
func (m *Monitor) ResourceSources(domain string) []upstreamrequest.ResourceSource {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshChannelLocked()
	if m.closed {
		return nil
	}
	if domain != "" {
		return m.sourcesLocked(Resource, domain, time.Now())
	}
	var sources []upstreamrequest.ResourceSource
	for _, host := range m.resourceIDsLocked() {
		sources = append(sources, m.sourcesLocked(Resource, host, time.Now())...)
	}
	upstreamrequest.SortResourceSources(sources)
	return sources
}

func (m *Monitor) removeResourceSourceLocked(key string) {
	delete(m.resourceSources, key)
	m.channel.ObserveResourceDomainsAtRevision(m.revision, "monitor/"+key, nil, 0)
}
