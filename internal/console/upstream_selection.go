package console

import (
	"context"
	"net/http"
	"slices"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

// Reserve a share of the caller's remaining deadline for every fallback.
// A single stalled channel must not consume the whole operation budget.
func channelAttempt(ctx context.Context, limit time.Duration, remaining int) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok {
		limit = min(limit, time.Until(deadline)/time.Duration(remaining))
	}
	return context.WithTimeout(ctx, limit)
}

func (c *Console) operationSelections(op upstreamstate.Operation, q upstreamstate.Constraints) []upstreamstate.Selection {
	c.mu.Lock()
	m, mode, client := c.monitor, channelMode(c.settings.UpstreamMode), c.client
	c.mu.Unlock()
	q.Mode = mode
	var selections []upstreamstate.Selection
	if m != nil {
		selections = m.Selections(op, q)
	}
	// Keep measured preferences first, then append unmeasured candidates from
	// the same configured mode. Partial checks must not disable fallbacks.
	if client != nil {
		if provider, ok := client.Transport.(upstreamrequest.Candidates); ok {
			target := q.Target
			if target == "" {
				target = q.Entry
			}
			for _, channel := range provider.Current(target) {
				if channel.Mode == mode && !slices.ContainsFunc(selections, func(s upstreamstate.Selection) bool { return s.Channel.ID == channel.ID }) {
					selections = append(selections, upstreamstate.Selection{Channel: channel})
				}
			}
		}
	}
	return selections[:min(len(selections), upstreamrequest.MaxOperationCandidates)]
}

func (c *Console) operationClients(ctx context.Context, op upstreamstate.Operation, q upstreamstate.Constraints) []*http.Client {
	base := c.upstreamClient()
	selections := c.operationSelections(op, q)
	if len(selections) == 0 {
		if provider, ok := base.Transport.(upstreamrequest.Candidates); ok {
			// Only cold direct discovery performs DNS; proxy discovery uses its
			// configured exits. Discovery shares the caller's operation deadline.
			current, _ := provider.Candidates(ctx, q.Entry)
			for _, channel := range current[:min(len(current), upstreamrequest.MaxOperationCandidates)] {
				selections = append(selections, upstreamstate.Selection{Channel: channel})
			}
		}
	}
	if len(selections) == 0 {
		// Legacy clients or a pool with no usable paths retain the ordinary
		// request. The caller's context still bounds discovery and attempts.
		return []*http.Client{base}
	}
	var clients []*http.Client
	for _, s := range selections {
		client := *base
		client.Transport = s.Channel.Transport
		clients = append(clients, &client)
	}
	return clients
}
