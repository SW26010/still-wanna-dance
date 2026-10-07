package upstreamrequest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func fixedTestProxy(id, body string) Proxy {
	return Proxy{ID: "stable-v1-" + strings.Repeat(id, 64), Dial: func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			if _, err := http.ReadRequest(bufio.NewReader(b)); err == nil {
				fmt.Fprintf(b, "HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
			}
		}()
		return a, nil
	}}
}

func TestMultiProxyConcurrentFixedEgressAndRetirement(t *testing.T) {
	proxies := []Proxy{fixedTestProxy("a", "exit A"), fixedTestProxy("b", "exit B")}
	p, err := NewMultiPool("socks5", nil, proxies)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	cs := p.Current("http://example.com/video")
	if len(cs) != 2 || cs[0].ID == cs[1].ID {
		t.Fatal(cs)
	}
	// Mutating the caller's slice must not redirect an existing generation.
	proxies[0] = proxies[1]
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			index := i % 2
			r, _ := http.NewRequest("GET", "http://example.com/video", nil)
			resp, err := cs[index].Transport.RoundTrip(r)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || string(body) != []string{"exit A", "exit B"}[index] {
				t.Errorf("wrong exit: %q %v", body, err)
			}
		}()
	}
	wg.Wait()
	p.Close()
	for _, c := range cs {
		r, _ := http.NewRequest("GET", "http://example.com/video", nil)
		if _, err := c.Transport.RoundTrip(r); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("retired candidate: %v", err)
		}
	}
}

func TestMultiProxyMembershipAndValidation(t *testing.T) {
	a, b := fixedTestProxy("a", "A"), fixedTestProxy("b", "B")
	for _, proxies := range [][]Proxy{{a, a}, {{ID: a.ID}}, {{ID: "display-name", Dial: a.Dial}}} {
		if _, err := NewMultiPool("socks5", nil, proxies); err == nil {
			t.Fatal("accepted invalid exits")
		}
	}
	p, err := NewMultiPool("socks5", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	cs, ready, err := p.CandidatesWithReadiness(context.Background(), "https://example.com")
	if len(cs) != 0 || !ready.Proxy || err != nil {
		t.Fatal(cs, ready, err)
	}
	old, _ := NewMultiPool("socks5", nil, []Proxy{a, b})
	defer old.Close()
	next, _ := NewMultiPool("socks5", nil, []Proxy{b, a})
	defer next.Close()
	if old.Current("https://example.com")[0].ID != next.Current("https://example.com")[1].ID {
		t.Fatal("ordering changed identity")
	}
}
