package upstreamstate

import (
	"context"
	"net/http"
)

func (m *Monitor) probeResourceWhenIdle(ctx context.Context, client *http.Client, p Policy, id int64, sample videoSample, automaticThroughput bool) observation {
	m.mu.Lock()
	manual := m.batchManual
	kind := m.batchKind
	m.mu.Unlock()
	if manual {
		throughput := kind != CheckLatency && kind != CheckConnectivity
		if throughput && ctx.Err() == nil {
			m.claimThroughput(true)
		}
		return probeResourceMode(ctx, client, p, id, sample, throughput)
	}
	var throughput bool
	firstAttempt := true
	for {
		probeCtx, finish, err := m.resources.ResourceProbe(ctx)
		if err != nil {
			return observation{op: Resource, route: sample.host, state: "canceled"}
		}
		if firstAttempt {
			throughput = automaticThroughput && m.claimThroughput(false)
			firstAttempt = false
		}
		o := probeResourceMode(probeCtx, client, p, id, sample, throughput)
		interrupted := finish()

		if ctx.Err() != nil {
			o.state = "canceled"
			return o
		}
		if !interrupted {
			return o
		}
		throughput = false
		// Discard measurements interrupted by business traffic, then retry
		// this channel when all business loads have released their slots.
	}
}
