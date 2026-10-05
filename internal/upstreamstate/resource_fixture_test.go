package upstreamstate

import "time"

// Synthetic resource observations in unit tests stand for a preceding API
// response. Production membership is populated only by playback observations.
func recordFixture(m *Monitor, o observation) {
	if o.op == Resource {
		if m.resourceSources == nil {
			m.resourceSources = make(map[string]observation)
		}
		m.resourceSources["fixture/"+o.route] = observation{resourceHost: o.route, at: time.Now()}
	}
	m.record(o)
}
