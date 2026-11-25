package console

import (
	"context"
	"net/http"
	"slices"
	"time"

	"still-wanna-dance/internal/upstreamnet"
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

func (c *Console) operationSelections(ctx context.Context, op upstreamstate.Operation, q upstreamstate.Constraints) []upstreamstate.Selection {
	c.mu.Lock()
	m, mode, client := c.monitor, c.settings.UpstreamMode, c.client
	c.mu.Unlock()
	if mode != "direct" {
		return nil
	}
	q.Mode = mode
	var selections []upstreamstate.Selection
	if m != nil {
		selections = m.Selections(op, q)
	}
	const limit = upstreamrequest.MaxOperationCandidates
	if len(selections) >= limit {
		return selections[:limit]
	}
	if client == nil {
		return selections
	}
	provider, ok := client.Transport.(upstreamrequest.Candidates)
	if !ok {
		return selections
	}
	target := q.Target
	if target == "" {
		target = q.Entry
	}
	current := provider.Current(target)
	if len(selections) == 0 && q.Entry != "" {
		switch {
		case len(current) == 0:
			current, _ = provider.Candidates(ctx, target)
		case op == upstreamstate.PlaybackURL && !slices.ContainsFunc(current, func(c upstreamrequest.Candidate) bool { return !c.ValidUntil.IsZero() }):
			// Only presets are ready. Reserve time to try them if discovery stalls.
			discovery, cancel := channelAttempt(ctx, upstreamnet.DefaultDNSPolicy().QueryTimeout, 2)
			discovered, _ := provider.Candidates(discovery, target)
			cancel()
			if len(discovered) > 0 {
				current = discovered
			}
		}
	}
	var fallbacks []upstreamrequest.Candidate
	seen := make(map[string]bool, len(selections))
	for _, s := range selections {
		seen[s.Channel.ID] = true
	}
	for _, c := range current {
		if c.Mode == mode && !seen[c.ID] {
			fallbacks = append(fallbacks, c)
			seen[c.ID] = true
		}
	}
	room := limit - len(selections)
	if op == upstreamstate.PlaybackURL {
		fallbacks = playbackFallbacks(fallbacks, room)
	}
	for _, c := range fallbacks[:min(len(fallbacks), room)] {
		selections = append(selections, upstreamstate.Selection{Channel: c})
	}
	return selections
}

// Extra presets may fill the request budget without displacing leased DNS paths.
func playbackFallbacks(paths []upstreamrequest.Candidate, limit int) []upstreamrequest.Candidate {
	if len(paths) > limit {
		var leased, presets []upstreamrequest.Candidate
		for _, c := range paths {
			if c.ValidUntil.IsZero() {
				presets = append(presets, c)
			} else {
				leased = append(leased, c)
			}
		}
		paths = paths[:0]
		for _, group := range [][]upstreamrequest.Candidate{leased, presets} {
			room := limit - len(paths)
			if room == 0 {
				break
			}
			if len(group) > room {
				upstreamrequest.OrderByFamily(group, true)
				group = group[:room]
			}
			paths = append(paths, group...)
		}
	}
	upstreamrequest.OrderByFamily(paths, false)
	return paths
}

func (c *Console) operationClients(ctx context.Context, op upstreamstate.Operation, q upstreamstate.Constraints) []*http.Client {
	base := c.upstreamClient()
	c.mu.Lock()
	proxy := c.settings.UpstreamMode == "proxy"
	c.mu.Unlock()
	if proxy {
		return []*http.Client{base}
	}
	selections := c.operationSelections(ctx, op, q)
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
