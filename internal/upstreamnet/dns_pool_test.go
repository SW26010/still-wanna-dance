package upstreamnet

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"golang.getoutline.org/sdk/transport"

	"still-wanna-dance/internal/catalogmeta"
	"still-wanna-dance/internal/playbacksource"
	"still-wanna-dance/internal/upstreamrequest"
)

func TestUdonPresetsShareDNSMembershipOnlyAtEligibleEndpoints(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := NewDirectResolver(nil)
		defer d.Close()
		d.setBackgroundEnabled(false)
		p, err := upstreamrequest.NewPool(d)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()

		play := playbacksource.Entries()[0].SongURL(playbacksource.DefaultBaseURL, 42)
		check := func(dns ...string) {
			t.Helper()
			for target, presets := range map[string][]string{
				catalogmeta.UdonURL:      defaultDirectPresetIPs,
				play:                     defaultDirectPresetIPs,
				catalogmeta.UdonHTTPURL:  nil,
				"http://api.udon.dance/": nil,
				"http://api.udon.dance:8080/Api/Songs/play": nil,
				"http://api.udon.dance/Api/Songs/%70lay":    nil,
			} {
				// DNS membership grants access to every endpoint until expiry.
				wantLease := make(map[string]bool)
				for _, ip := range presets {
					wantLease[ip] = false
				}
				for _, ip := range dns {
					wantLease[ip] = true
				}
				for _, c := range p.Current(target) {
					leased, ok := wantLease[c.IP]
					hasLease := !c.ValidUntil.IsZero()
					if !ok || leased != hasLease {
						t.Fatalf("%s: unexpected or duplicate candidate %+v", target, c)
					}
					delete(wantLease, c.IP)
				}
				if len(wantLease) != 0 {
					t.Fatalf("%s: missing candidates %v", target, wantLease)
				}
			}
		}
		check()
		answers := []string{"192.0.2.1", defaultDirectPresetIPs[0], defaultDirectPresetIPs[1]}
		if _, err := d.lookupShared(t.Context(), catalogmeta.UdonHost, func(context.Context) ([]string, time.Duration, error) {
			return answers, time.Second, nil
		}); err != nil {
			t.Fatal(err)
		}
		check(answers...)
		time.Sleep(2 * time.Second)
		check()
	})
}

func TestResourcePresetsSupplementDNSWithoutExtendingLeases(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const host = playUdonDanceHost
		overlap := defaultDirectPresetIPs[0]
		const target = "http://" + host + "/video"
		d := NewDirectResolver(nil)
		defer d.Close()
		d.setBackgroundEnabled(false)
		dialError := errors.New("fixture dial")
		p, err := upstreamrequest.NewDirectPool(d, func(_ context.Context, _, address string) (net.Conn, error) {
			if address != net.JoinHostPort(overlap, "80") {
				t.Errorf("unexpected dial address %q", address)
			}
			return nil, dialError
		})
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		if len(p.Current(target)) != len(defaultDirectPresetIPs) {
			t.Fatal("presets should be available before DNS discovery")
		}
		ctx := context.Background()
		query := func(context.Context) ([]string, time.Duration, error) {
			return []string{"2001:db8::1", overlap}, time.Second, nil
		}
		for _, name := range []string{host, "api.udon.dance"} {
			if _, err := d.lookupShared(ctx, name, query); err != nil {
				t.Fatal(err)
			}
		}
		if got := p.Current("http://api.udon.dance"); len(got) != 2 {
			t.Fatalf("presets leaked to the API host: %+v", got)
		}
		cs := p.Current(target)
		i := slices.IndexFunc(cs, func(c upstreamrequest.Candidate) bool { return c.IP == overlap })
		if len(cs) != len(defaultDirectPresetIPs)+1 || i < 0 || cs[i].ValidUntil.IsZero() || net.ParseIP(cs[0].IP).To4() != nil {
			t.Fatalf("expected one candidate for the overlapping IP with its DNS lease: %+v", cs)
		}

		time.Sleep(2 * time.Second)
		dnsError := errors.New("fixture DNS offline")
		if _, err := d.lookupShared(ctx, host, func(context.Context) ([]string, time.Duration, error) {
			return nil, 0, dnsError
		}); !errors.Is(err, dnsError) {
			t.Fatal(err)
		}
		var ips []string
		for _, c := range p.Current(target) {
			ips = append(ips, c.IP)
			if !c.ValidUntil.IsZero() {
				t.Errorf("candidate %s should rely on the preset alone", c.IP)
			}
		}
		want := slices.Clone(defaultDirectPresetIPs)
		slices.Sort(ips)
		slices.Sort(want)
		if !slices.Equal(ips, want) {
			t.Fatalf("candidates after DNS expiry and failure = %v, want %v", ips, want)
		}
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cs[i].Transport.RoundTrip(req); !errors.Is(err, dialError) {
			t.Fatalf("overlapping candidate did not dial the preset after expiry: %v", err)
		}
	})
}

func TestFixedResourceRequestsStartAndResumeDNS(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const target = "http://" + playUdonDanceHost + "/video"
		d := NewDirectResolver(nil)
		defer d.Close()
		var dnsDials atomic.Int32
		d.dialer = transport.FuncStreamDialer(func(ctx context.Context, _ string) (transport.StreamConn, error) {
			dnsDials.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		d.policy = DNSPolicy{MinInterval: 10 * time.Second, IdleTimeout: 5 * time.Second, QueryTimeout: time.Second}
		pinnedError := errors.New("fixture pinned dial")
		pool, err := upstreamrequest.NewDirectPool(d, func(context.Context, string, string) (net.Conn, error) {
			return nil, pinnedError
		})
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()

		dnsActive := func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.states[playUdonDanceHost] != nil
		}
		preset := pool.Current(target)[0]
		request := func() error {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
			if err != nil {
				return err
			}
			_, err = preset.Transport.RoundTrip(req)
			return err
		}
		start := time.Now()
		if err := request(); !errors.Is(err, pinnedError) || !time.Now().Equal(start) {
			t.Fatalf("cold preset waited for DNS: %v", err)
		}
		synctest.Wait()
		if dnsDials.Load() == 0 {
			t.Fatal("first fixed request did not start DNS")
		}
		time.Sleep(4 * time.Second)
		before := dnsDials.Load()
		if err := request(); !errors.Is(err, pinnedError) {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if dnsDials.Load() != before || !dnsActive() {
			t.Errorf("recent fixed request should keep DNS active at the original idle boundary without refreshing early")
		}

		time.Sleep(4 * time.Second)
		synctest.Wait()
		if got := pool.Current(target); len(got) != len(defaultDirectPresetIPs) {
			t.Errorf("idle DNS cleanup removed configured candidates: got %d", len(got))
		}
		if dnsActive() || dnsDials.Load() != before {
			t.Fatal("reading candidates should leave DNS idle")
		}
		start = time.Now()
		if err := request(); !errors.Is(err, pinnedError) || !time.Now().Equal(start) {
			t.Errorf("fixed request waited for DNS: elapsed=%v, err=%v", time.Since(start), err)
		}
		synctest.Wait()
		if dnsDials.Load() == before {
			t.Fatal("fixed request did not resume background DoH")
		}
	})
}

func TestDNSPoolRetainsBothFamilies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ips, ttl, err := collectDNS(context.Background(), []dnsQuery{
			func(context.Context) ([]string, time.Duration, error) {
				return []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5", "192.0.2.6"}, time.Minute, nil
			},
			func(context.Context) ([]string, time.Duration, error) {
				time.Sleep(100 * time.Millisecond)
				return []string{"2001:db8::1", "2001:db8::2", "2001:db8::3", "2001:db8::4"}, 10 * time.Second, nil
			},
		})
		v6 := 0
		for _, ip := range ips {
			if net.ParseIP(ip).To4() == nil {
				v6++
			}
		}
		if err != nil || len(ips) != 6 || v6 != 3 || ttl != 10*time.Second {
			t.Fatal(ips, ttl, err)
		}
	})
}

func TestDNSPoolSingleFamilyDoesNotWaitForOtherFamily(t *testing.T) {
	for _, ip := range []string{"192.0.2.1", "2001:db8::1"} {
		synctest.Test(t, func(t *testing.T) {
			start := time.Now()
			ips, ttl, err := collectDNS(context.Background(), []dnsQuery{
				func(context.Context) ([]string, time.Duration, error) { return []string{ip}, time.Minute, nil },
				func(ctx context.Context) ([]string, time.Duration, error) { <-ctx.Done(); return nil, 0, ctx.Err() },
			})
			if err != nil || !slices.Equal(ips, []string{ip}) || ttl != time.Minute || time.Since(start) != 200*time.Millisecond {
				t.Fatal(ips, ttl, err, time.Since(start))
			}
		})
	}
}

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

func TestDNSPoolEmptyFamily(t *testing.T) {
	empty := func(context.Context) ([]string, time.Duration, error) { return nil, 0, nil }
	for _, available := range [][]string{nil, {"192.0.2.1"}, {"2001:db8::1"}} {
		ips, ttl, err := collectDNS(context.Background(), []dnsQuery{
			empty,
			func(context.Context) ([]string, time.Duration, error) { return available, time.Minute, nil },
		})
		if len(available) == 0 {
			if err == nil || err.Error() != "no usable DNS answers" || len(ips) != 0 || ttl != 0 {
				t.Fatal("all-empty result must reject", ips, ttl, err)
			}
		} else if err != nil || !slices.Equal(ips, available) || ttl != time.Minute {
			t.Fatal("empty family affected valid answers", ips, ttl, err)
		}
	}
}

func TestDNSDialRacesCandidatesWithoutRankingDNS(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	d := &Resolver{cache: map[string]dnsEntry{"race.invalid": {[]string{"127.0.0.2", "127.0.0.1"}, time.Now().Add(time.Minute)}}}
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
