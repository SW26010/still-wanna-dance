package upstreamstate

import "strings"

// Called with m.mu held after candidate discovery, independently of history.
func (m *Monitor) pruneThroughputLocked(valid map[string]bool, prepared map[string]bool) {
	for key := range m.throughput {
		parts := strings.SplitN(key, "/", 3)
		if len(parts) < 2 || valid[key] {
			continue
		}
		canPrune := prepared[entry(Resource, parts[1])]
		if !canPrune {
			continue
		}
		delete(m.throughput, key)
	}
}
