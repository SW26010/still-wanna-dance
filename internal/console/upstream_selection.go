package console

import (
	"context"
	"net/http"
	"strings"
	"time"

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
	if m != nil {
		if selections := m.Selections(op, q); len(selections) > 0 {
			return selections
		}
	}
	// Cold Mihomo traffic uses the same fixed candidates as Monitor. It does not
	// wait for a probe, silently use the old SOCKS backend, or invent a score.
	if client != nil {
		if runtime, ok := client.Transport.(*mihomoTransport); ok {
			target := q.Target
			if target == "" {
				target = q.Entry
			}
			var selections []upstreamstate.Selection
			for _, channel := range runtime.Current(target) {
				if q.Mode == "" || channel.Mode == q.Mode {
					selections = append(selections, upstreamstate.Selection{Channel: channel})
				}
			}
			return selections
		}
	}
	return nil
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
		if len(clients) == 6 {
			break
		}
	}
	return clients
}

func (c *Console) playbackRoutes(preferred, mode string) []string {
	if mode == "cf" || mode == "hkg" {
		return []string{mode}
	}
	routes := []string{"hkg", "cf"}
	if preferred == "cf" {
		routes = []string{"cf", "hkg"}
	}
	for _, s := range c.operationSelections(upstreamstate.PlaybackURL, upstreamstate.Constraints{}) {
		if strings.HasPrefix(s.Result.Entry, c.apiBase+"/Api/Songs/play?") {
			if s.Result.Route != routes[0] && s.Result.Route == routes[1] {
				routes[0], routes[1] = routes[1], routes[0]
			}
			break
		}
	}
	return routes
}
