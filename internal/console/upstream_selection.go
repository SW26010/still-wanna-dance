package console

import (
	"context"
	"net/http"
	"net/url"
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
	if mode == "direct" || mode == "socks5" {
		q.Mode = mode
	}
	var selections []upstreamstate.Selection
	if m != nil {
		selections = m.Selections(op, q)
	}
	// Keep measured preferences first, then append the remaining active Mihomo
	// candidates. Partial observations must not disable business fallbacks.
	if client != nil {
		if runtime, ok := client.Transport.(*mihomoTransport); ok && !runtime.retired.Load() {
			target := q.Target
			if target == "" {
				target = q.Entry
			}
			current := runtime.Current(target)
			measured := len(selections) > 0
			for _, channel := range current {
				if (q.Mode == "" || channel.Mode == q.Mode) &&
					!slices.ContainsFunc(selections, func(s upstreamstate.Selection) bool { return s.Channel.ID == channel.ID }) {
					selections = append(selections, upstreamstate.Selection{Channel: channel})
				}
			}
			u, err := url.Parse(target)
			if runtime.direct != nil && q.Mode != "socks5" && err == nil && u.Hostname() != "" && (u.Scheme == "http" || u.Scheme == "https") &&
				!slices.ContainsFunc(current, func(c upstreamrequest.Candidate) bool { return c.Mode == "direct" }) {
				fallback := upstreamstate.Selection{Channel: upstreamrequest.Candidate{Host: u.Hostname(), Mode: "direct", Transport: runtime.direct}}
				if measured {
					selections = append(selections, fallback)
				} else {
					selections = append([]upstreamstate.Selection{fallback}, selections...)
				}
			}
		}
	}
	return boundedSelections(selections)
}

// Keep Monitor's relative order, reserving space for the active proxy exits and
// at least one direct path in Auto. Multiple direct IPs must not crowd out every
// proxy (or the fifth active node) before either API or download attempts start.
func boundedSelections(selections []upstreamstate.Selection) []upstreamstate.Selection {
	limit := upstreamrequest.MaxOperationCandidates
	if len(selections) <= limit {
		return selections
	}
	proxies := 0
	for _, s := range selections {
		if s.Channel.Mode == "socks5" {
			proxies++
		}
	}
	directBudget := max(1, limit-proxies)
	if proxies == len(selections) {
		directBudget = 0
	}
	proxyBudget := limit - directBudget
	var out []upstreamstate.Selection
	for _, s := range selections {
		if s.Channel.Mode == "direct" && directBudget > 0 {
			out = append(out, s)
			directBudget--
		} else if s.Channel.Mode == "socks5" && proxyBudget > 0 {
			out = append(out, s)
			proxyBudget--
		}
	}
	return out
}

func (c *Console) operationClients(op upstreamstate.Operation, q upstreamstate.Constraints) []*http.Client {
	base := c.upstreamClient()
	selections := c.operationSelections(op, q)
	if len(selections) == 0 {
		// Cold/expired observations use the configured channel once. Never
		// trigger an ad-hoc measurement or race from a business request.
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
