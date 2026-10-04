package console

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"
	"time"
)

func TestDNSPoolMergesAnswersWithMinimumTTL(t *testing.T) {
	ips, ttl, err := collectDNS(context.Background(), []dnsQuery{
		func(context.Context) ([]string, time.Duration, error) {
			return []string{"203.0.113.1", "203.0.113.2"}, time.Minute, nil
		},
		func(context.Context) ([]string, time.Duration, error) {
			return []string{"203.0.113.2", "203.0.113.3"}, 10 * time.Second, nil
		},
		func(context.Context) ([]string, time.Duration, error) { return nil, 0, errors.New("offline") },
	})
	slices.Sort(ips)
	if err != nil || ttl != 10*time.Second || !slices.Equal(ips, []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"}) {
		t.Fatal(ips, ttl, err)
	}
}

func TestDNSPoolDoesNotWaitForUnavailableProvider(t *testing.T) {
	finished := make(chan struct{})
	start := time.Now()
	ips, _, err := collectDNS(context.Background(), []dnsQuery{
		func(context.Context) ([]string, time.Duration, error) {
			return []string{"203.0.113.1"}, time.Minute, nil
		},
		func(ctx context.Context) ([]string, time.Duration, error) {
			<-ctx.Done()
			close(finished)
			return nil, 0, ctx.Err()
		},
	})
	if err != nil || len(ips) != 1 || time.Since(start) > time.Second {
		t.Fatal(ips, err, time.Since(start))
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("provider not canceled")
	}
}

func TestDNSPoolAllEncryptedFailuresReject(t *testing.T) {
	ips, _, err := collectDNS(context.Background(), []dnsQuery{
		func(context.Context) ([]string, time.Duration, error) {
			return nil, 0, errors.New("certificate rejected")
		},
		func(context.Context) ([]string, time.Duration, error) { return nil, 0, errors.New("unreachable") },
	})
	if err == nil || len(ips) != 0 {
		t.Fatal("failed encrypted resolution produced usable addresses", ips, err)
	}
}
func TestDNSDialRacesCandidatesWithoutRankingDNS(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	d := &directDNS{cache: map[string]dnsEntry{"race.invalid": {[]string{"127.0.0.2", "127.0.0.1"}, time.Now().Add(time.Minute)}}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := d.DialContext(ctx, "tcp4", net.JoinHostPort("race.invalid", port))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	d.mu.Lock()
	first := d.cache["race.invalid"].ips[0]
	d.mu.Unlock()
	if first != "127.0.0.2" {
		t.Fatal(first)
	}
}
