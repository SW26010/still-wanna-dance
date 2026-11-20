package upstreamstate

import (
	"context"
	"net/http"

	"still-wanna-dance/internal/upstreamcheck"
	"still-wanna-dance/internal/upstreamrequest"
)

// NewExperimentMonitor enables the per-candidate measurements used by isolated
// path comparisons. The application Monitor only requests catalogs and playback URLs.
func NewExperimentMonitor(o Options) (*Monitor, error) {
	m, err := NewMonitor(o)
	if err == nil {
		m.experimentProbe = m.checkExperimentResources
	}
	return m, err
}

func probeResourceMode(ctx context.Context, client *http.Client, p Policy, id int64, s videoSample, throughput bool) observation {
	return observed(upstreamcheck.ProbeResource(ctx, client, probePolicy(p), id, upstreamcheck.Sample{URL: s.url, Host: s.host, Checksum: s.checksum, Size: s.size}, throughput))
}

type experimentResourceProbe func(context.Context, Policy, int64, CheckKind, func(chan<- videoSample), func(string) []upstreamrequest.Candidate, func(observation, upstreamrequest.Candidate))

func (m *Monitor) checkExperimentResources(ctx context.Context, p Policy, id int64, kind CheckKind, playback func(chan<- videoSample), prepare func(string) []upstreamrequest.Candidate, publish func(observation, upstreamrequest.Candidate)) {
	samples := make(chan videoSample)
	go func() {
		defer close(samples)
		playback(samples)
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
			throughput := kind != CheckLatency && kind != CheckConnectivity
			publish(probeResourceMode(ctx, requestClient(c.Transport), p, id, sample, throughput), c)
		}
	}
}
