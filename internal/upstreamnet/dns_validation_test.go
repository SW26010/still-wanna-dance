package upstreamnet

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"testing/synctest"
	"time"
)

func TestDNSValidation(t *testing.T) {
	var answer dnsAnswer
	if err := json.Unmarshal([]byte(`{"Status":0,"Answer":[{"type":1,"TTL":60,"data":"127.0.0.1"},{"type":1,"TTL":120,"data":"203.0.113.9"}]}`), &answer); err != nil {
		t.Fatal(err)
	}
	ips, ttl, err := dnsAddresses(answer)
	if err != nil || len(ips) != 1 || ips[0] != "203.0.113.9" || ttl != 120*time.Second {
		t.Fatal(ips, ttl, err)
	}
	// An unresolvable OS name still connects via the independent resolver cache.
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	d := &Resolver{cache: map[string]dnsEntry{"hosts-must-not-be-used.invalid": {[]string{"127.0.0.1"}, time.Now().Add(time.Minute)}}}
	defer d.Close()
	conn, err := d.DialContext(context.Background(), "tcp", "hosts-must-not-be-used.invalid:"+port)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestCNAMECacheTTL(t *testing.T) {
	for _, ttl := range []int{0, 15, 180} {
		t.Run(fmt.Sprint(ttl), func(t *testing.T) {
			var answer dnsAnswer
			body := fmt.Sprintf(`{"Status":0,"Answer":[{"Type":5,"TTL":90,"Data":"alias.test"},{"Type":5,"TTL":%d,"Data":"target.test"},{"Type":1,"TTL":120,"Data":"203.0.113.9"}]}`, ttl)
			if err := json.Unmarshal([]byte(body), &answer); err != nil {
				t.Fatal(err)
			}
			ips, duration, err := dnsAddresses(answer)
			want := time.Duration(min(ttl, 90)) * time.Second
			if err != nil || len(ips) != 1 || duration != want {
				t.Fatal(ips, duration, err)
			}
		})
	}
}

func TestWireCNAMECacheTTL(t *testing.T) {
	q := []byte{0x12, 0x34, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 3, 'a', 'p', 'i', 4, 't', 'e', 's', 't', 0, 0, 1, 0, 1}
	for _, ttl := range []byte{0, 15, 180} {
		reply := append([]byte(nil), q...)
		reply[2] = 0x81
		reply[3] = 0x80
		reply[7] = 2
		// The address precedes the alias, so TTL calculation must examine all answers.
		reply = append(reply, 0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 120, 0, 4, 198, 18, 0, 8)
		reply = append(reply, 0xc0, 12, 0, 5, 0, 1, 0, 0, 0, ttl, 0, 2, 0xc0, 12)
		ips, duration, err := parseDNSReply(q, reply)
		if err != nil || len(ips) != 1 || duration != time.Duration(min(ttl, 120))*time.Second {
			t.Fatal(ips, duration, err)
		}
	}
}
func TestDNSPoolOverlapsAnswersOnlyUntilOriginalTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &Resolver{}
		defer d.Close()
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
