// Package playbacksource owns the allowed playback entries and their protocol.
// Consumers request song addresses; connection adapters only supply HTTP clients.
// An entry identifies API provenance, never the CDN of its returned resource.
package playbacksource

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"still-wanna-dance/internal/videometa"
)

const DefaultBaseURL = "https://api.udon.dance"

type Entry struct{ ID, Node string }

// Entries is the authoritative source policy, in resolution preference order.
// Return a fresh slice so diagnostics cannot change business eligibility.
func Entries() []Entry { return []Entry{{ID: "cf", Node: "cf"}} }

// Nodes supplies the same policy to persisted-address and metadata queries.
func Nodes() []string {
	nodes := make([]string, 0, len(Entries()))
	for _, entry := range Entries() {
		nodes = append(nodes, entry.Node)
	}
	return nodes
}

func (e Entry) Endpoint(base string) string {
	return base + "/Api/Songs/play?node=" + e.Node
}

func (e Entry) SongURL(base string, id int64) string {
	return e.Endpoint(base) + "&id=" + strconv.FormatInt(id, 10)
}

// Address is validated API evidence. It does not certify downloaded bytes.
type Address struct {
	SongID                     int64
	URL, API, Node, ChannelID  string
	QueryStartedAt, ObservedAt time.Time
}

// Resolver has no cache or background lifetime. Its caller owns cancellation
// and persistence; Clients owns network configuration and channel ranking.
type Resolver struct {
	BaseURL      string
	MaxFileBytes int64
	// AttemptTimeout must be positive. The remaining caller deadline also bounds each attempt.
	AttemptTimeout time.Duration
	// Clients must supply API clients that stop before following resource redirects.
	// An empty result is unavailable; the resolver never creates a fallback client.
	Clients func(context.Context, string) []*http.Client
}

func (r Resolver) Resolve(ctx context.Context, id int64) (Address, error) {
	if id <= 0 {
		return Address{}, errors.New("invalid song ID")
	}
	base := r.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	var failures []error
	for _, entry := range Entries() {
		if err := ctx.Err(); err != nil {
			return Address{}, err
		}
		clients := r.Clients(ctx, entry.Endpoint(base))
		for i, client := range clients {
			if err := ctx.Err(); err != nil {
				return Address{}, err
			}
			budget := r.AttemptTimeout
			if deadline, ok := ctx.Deadline(); ok {
				budget = min(budget, time.Until(deadline)/time.Duration(len(clients)-i))
			}
			attempt, cancel := context.WithTimeout(ctx, budget)
			address, err := fetch(attempt, client, base, entry, id, r.MaxFileBytes)
			cancel()
			if err == nil {
				return address, nil
			}
			failures = append(failures, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return Address{}, err
	}
	if len(failures) == 0 {
		return Address{}, errors.New("no playback source available")
	}
	return Address{}, errors.Join(failures...)
}

func fetch(ctx context.Context, client *http.Client, base string, entry Entry, id, maxBytes int64) (Address, error) {
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.SongURL(base, id), nil)
	if err != nil {
		return Address{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Address{}, err
	}
	defer resp.Body.Close()
	u, _, err := videometa.PlaybackLocation(resp, maxBytes)
	if err != nil {
		return Address{}, fmt.Errorf("playback source %s: %w", entry.ID, err)
	}
	address := Address{SongID: id, URL: u.String(), API: base + "/Api/Songs/play", Node: entry.Node, QueryStartedAt: started, ObservedAt: time.Now()}
	if channel, ok := client.Transport.(interface{ ChannelID() string }); ok {
		address.ChannelID = channel.ChannelID()
	}
	return address, nil
}
