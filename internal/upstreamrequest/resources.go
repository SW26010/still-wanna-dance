package upstreamrequest

import (
	"net/url"
	"sort"
	"time"

	"still-wanna-dance/internal/videometa"
)

// ResourceSource preserves a validated API response before domain aggregation.
// ResourceURL is available to internal consumers but never serialized into status
// JSON: its query may contain playback signatures. Node is the requested value.
type ResourceSource struct {
	API          string    `json:"api"`
	Node         string    `json:"node"`
	SongID       int64     `json:"songID"`
	APIChannelID string    `json:"apiChannelID,omitempty"`
	ResourceURL  string    `json:"-"`
	Domain       string    `json:"domain"`
	Path         string    `json:"path"`
	ObservedAt   time.Time `json:"observedAt"`
	ValidUntil   time.Time `json:"validUntil"`
}

type resourceObservation struct {
	sources []ResourceSource
	hosts   []string
	until   time.Time
}

// ObserveResourceDomains is called only after API response validation. Each
// source replaces its previous set. DNS, client Host and persisted statistics
// must never create resource membership.
func (c *Channel) ObserveResourceDomains(source string, hosts []string, lifetime time.Duration) {
	c.ObserveResourceDomainsAtRevision(c.Snapshot().Revision, source, hosts, lifetime)
}

// ObserveResourceDomainsAtRevision rejects responses from retired transports.
func (c *Channel) ObserveResourceDomainsAtRevision(revision uint64, source string, hosts []string, lifetime time.Duration) {
	c.observeResources(revision, source, hosts, nil, lifetime)
}

// ObserveResourceSourcesAtRevision retains all issuing APIs, even when their
// returned domains or URLs are identical. Call only after response validation.
func (c *Channel) ObserveResourceSourcesAtRevision(revision uint64, source string, sources []ResourceSource, lifetime time.Duration) {
	var hosts []string
	var valid []ResourceSource
	for _, item := range sources {
		u, err := url.Parse(item.ResourceURL)
		if err != nil || !videometa.ValidHost(u.Host) || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
			continue
		}
		if _, err := videometa.Parse(u, 2<<30); err != nil {
			continue
		}
		item.Domain, item.Path = u.Host, u.Path
		if item.ObservedAt.IsZero() {
			item.ObservedAt = time.Now()
		}
		if item.ValidUntil.IsZero() {
			item.ValidUntil = item.ObservedAt.Add(lifetime)
		}
		hosts = append(hosts, item.Domain)
		valid = append(valid, item)
	}
	c.observeResources(revision, source, hosts, valid, lifetime)
}

func (c *Channel) observeResources(revision uint64, source string, hosts []string, sources []ResourceSource, lifetime time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.revision != revision {
		return
	}
	if c.resourceDomains == nil {
		c.resourceDomains = make(map[string]resourceObservation)
	}
	for key, o := range c.resourceDomains {
		if !time.Now().Before(o.until) {
			delete(c.resourceDomains, key)
		}
	}
	var valid []string
	for _, host := range hosts {
		if videometa.ValidHost(host) {
			valid = append(valid, host)
		}
	}
	if len(valid) == 0 || lifetime <= 0 {
		delete(c.resourceDomains, source)
		return
	}
	if _, replacing := c.resourceDomains[source]; !replacing && len(c.resourceDomains) >= 512 {
		var oldest string
		var until time.Time
		for key, o := range c.resourceDomains {
			if until.IsZero() || o.until.Before(until) {
				oldest, until = key, o.until
			}
		}
		delete(c.resourceDomains, oldest)
	}
	c.resourceDomains[source] = resourceObservation{hosts: valid, sources: append([]ResourceSource(nil), sources...), until: time.Now().Add(lifetime)}
}

func (c *Channel) ResourceDomains() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneResourceSourcesLocked(time.Now())
	seen := make(map[string]bool)
	for _, o := range c.resourceDomains {
		if time.Now().Before(o.until) {
			for _, host := range o.hosts {
				seen[host] = true
			}
		}
	}
	var hosts []string
	for host := range seen {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

func (c *Channel) IsResourceDomain(host string) bool {
	for _, known := range c.ResourceDomains() {
		if host == known {
			return true
		}
	}
	return false
}

// ResourceSources returns independent values for every live issuing source. An
// empty domain selects all resources; filter by Node or ResourceURL as needed.
func (c *Channel) ResourceSources(domain string) []ResourceSource {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneResourceSourcesLocked(time.Now())
	var sources []ResourceSource
	now := time.Now()
	for _, o := range c.resourceDomains {
		if !now.Before(o.until) {
			continue
		}
		for _, item := range o.sources {
			if (domain == "" || item.Domain == domain) && now.Before(item.ValidUntil) {
				sources = append(sources, item)
			}
		}
	}
	SortResourceSources(sources)
	return sources
}

// ResourceSourceActive uses only current candidate state, never DNS I/O. A
// retired issuing channel cannot authorize a resource until it resolves again.
func ResourceSourceActive(s ResourceSource, provider Candidates, now time.Time) bool {
	if !now.Before(s.ValidUntil) {
		return false
	}
	if s.APIChannelID == "" {
		return true
	}
	if provider == nil {
		return false
	}
	for _, candidate := range provider.Current(s.API) {
		if candidate.ID == s.APIChannelID && (candidate.ValidUntil.IsZero() || now.Before(candidate.ValidUntil)) {
			return true
		}
	}
	return false
}

func (c *Channel) pruneResourceSourcesLocked(now time.Time) {
	provider, _ := c.transport.(Candidates)
	for key, observation := range c.resourceDomains {
		if !now.Before(observation.until) {
			delete(c.resourceDomains, key)
			continue
		}
		if len(observation.sources) == 0 {
			continue
		}
		var sources []ResourceSource
		var hosts []string
		for _, s := range observation.sources {
			if ResourceSourceActive(s, provider, now) {
				sources = append(sources, s)
				hosts = append(hosts, s.Domain)
			}
		}
		if len(sources) == 0 {
			delete(c.resourceDomains, key)
			continue
		}
		observation.sources, observation.hosts = sources, hosts
		c.resourceDomains[key] = observation
	}
}

func SortResourceSources(sources []ResourceSource) {
	sort.Slice(sources, func(i, j int) bool {
		a, b := sources[i], sources[j]
		if a.API != b.API {
			return a.API < b.API
		}
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		if a.SongID != b.SongID {
			return a.SongID < b.SongID
		}
		if a.APIChannelID != b.APIChannelID {
			return a.APIChannelID < b.APIChannelID
		}
		if a.ResourceURL != b.ResourceURL {
			return a.ResourceURL < b.ResourceURL
		}
		return a.ObservedAt.Before(b.ObservedAt)
	})
}
