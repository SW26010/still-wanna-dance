package upstreamstate

import (
	"context"
	"net/http"
)

func (m *Monitor) probeResourceWhenIdle(ctx context.Context, client *http.Client, p Policy, id int64, r route, sample videoSample) observation {
	m.mu.Lock()
	manual := m.batchManual
	kind := m.batchKind
	m.mu.Unlock()
	if manual {
		if kind != CheckLatency && ctx.Err() == nil {
			m.claimThroughput(true)
		}
		return probeResourceMode(ctx, client, p, id, r, sample, kind != CheckLatency)
	}
	var throughput bool
	firstAttempt := true
	for {
		probeCtx, finish, err := m.channel.ResourceProbe(ctx)
		if err != nil {
			return observation{op: Resource, route: r.id, state: "canceled"}
		}
		if firstAttempt {
			throughput = m.claimThroughput(false)
			firstAttempt = false
		}
		o := probeResourceMode(probeCtx, client, p, id, r, sample, throughput)
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
