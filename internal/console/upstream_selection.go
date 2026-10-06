package console

import (
	"net/http"
	"strings"

	"still-wanna-dance/internal/upstreamstate"
)

func (c *Console) operationSelections(op upstreamstate.Operation, q upstreamstate.Constraints) []upstreamstate.Selection {
	c.mu.Lock()
	m, mode := c.monitor, c.settings.UpstreamMode
	c.mu.Unlock()
	if m == nil {
		return nil
	}
	if mode == "direct" || mode == "socks5" {
		q.Mode = mode
	}
	return m.Selections(op, q)
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
		if len(clients) == 4 {
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
