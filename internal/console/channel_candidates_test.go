package console

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestChannelModesFollowSettingsAndRetireSavedCandidates(t *testing.T) {
	c := testConsole(t)
	c.dns.mu.Lock()
	c.dns.cache = map[string]dnsEntry{"api.udon.dance": {[]string{"203.0.113.1"}, time.Now().Add(time.Minute)}}
	c.dns.mu.Unlock()
	target := "https://api.udon.dance/Api/Songs/list"
	initial := upstreamrequest.Default.Snapshot()
	direct := initial.Candidates.Current(target)
	if len(direct) != 1 || direct[0].Mode != "direct" {
		t.Fatal(direct)
	}
	s := c.settings
	s.UpstreamMode = "auto"
	s.SOCKS5Address = "127.0.0.1:1080"
	if err := c.saveSettings(s, false); err != nil {
		t.Fatal(err)
	}
	both := upstreamrequest.Default.Snapshot().Candidates.Current(target)
	if len(both) != 2 || both[0].Mode != "direct" || both[1].Mode != "socks5" {
		t.Fatal(both)
	}
	r, _ := http.NewRequest("GET", target, nil)
	if _, err := direct[0].Transport.RoundTrip(r); !errors.Is(err, upstreamrequest.ErrUnavailable) {
		t.Fatal("old candidate survived config replacement", err)
	}
	s.UpstreamMode = "socks5"
	if err := c.saveSettings(s, false); err != nil {
		t.Fatal(err)
	}
	proxy, err := upstreamrequest.Default.Snapshot().Candidates.Candidates(context.Background(), "https://never-resolve.invalid")
	if err != nil || len(proxy) != 1 || proxy[0].Mode != "socks5" {
		t.Fatal(proxy, err)
	}
	c.dns.mu.Lock()
	_, looked := c.dns.states["never-resolve.invalid"]
	c.dns.mu.Unlock()
	if looked {
		t.Fatal("forced SOCKS5 invoked local DNS")
	}
}

func TestDNSPoolOverlapsAnswersOnlyUntilOriginalTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &directDNS{}
		defer d.close()
		p := DefaultDNSPolicy()
		p.MinInterval = 10 * time.Second
		p.RefreshAhead = 20 * time.Second
		if err := d.setPolicy(p); err != nil {
			t.Fatal(err)
		}
		calls := 0
		query := func(context.Context) ([]string, time.Duration, error) {
			calls++
			if calls == 1 {
				return []string{"203.0.113.1"}, 30 * time.Second, nil
			}
			return []string{"203.0.113.2"}, time.Minute, nil
		}
		_, _ = d.lookupShared(context.Background(), "example.com", query)
		changed := d.DNSChanged()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		select {
		case <-changed:
		default:
			t.Fatal("new IP did not notify channel")
		}
		addresses := d.CurrentAddresses("example.com")
		if len(addresses) != 2 {
			t.Fatal("no overlap during refresh", addresses)
		}
		time.Sleep(20 * time.Second)
		synctest.Wait()
		addresses = d.CurrentAddresses("example.com")
		if len(addresses) != 1 || addresses[0].IP != "203.0.113.2" {
			t.Fatal("old TTL extended", addresses)
		}
	})
}
