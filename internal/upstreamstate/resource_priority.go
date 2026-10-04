package upstreamstate

import (
	"context"
	"net/http"
)

func (m *Monitor) probeResourceWhenIdle(ctx context.Context, client *http.Client, p Policy, id int64, r route, sample videoSample) observation {
	m.mu.Lock()
	manual := m.batchManual
	m.mu.Unlock()
	if manual {
		return probeResource(ctx, client, p, id, r, sample)
	}
	for {
		probeCtx, finish, err := m.channel.ResourceProbe(ctx)
		if err != nil {
			return observation{op: Resource, route: r.id, state: "canceled"}
		}
		o := probeResource(probeCtx, client, p, id, r, sample)
		interrupted := finish()

		if ctx.Err() != nil {
			o.state = "canceled"
			return o
		}
		if !interrupted {
			return o
		}
		// Discard measurements interrupted by business traffic, then retry
		// this channel when all business loads have released their slots.
	}
}
